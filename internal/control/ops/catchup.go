package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tianwei/diskless/internal/ha"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/zfs"
)

// 递归增量收不下时，用逐数据集追平代替整体重建。
// 超管保存或配置合并会把数据集换成同名的另一个，跨越替换的那一轮递归流会被
// 接收端拒收（只影响这一轮）。单数据集 `send -I` 只校验起点快照，而 promote
// 保留了旧快照的 GUID，所以可以逐个追到同一标记，下一轮再恢复递归。

// errCatchUpDiverged 表示追平会回滚本机自己写入的内容，交给分叉处理决定保留哪边。
var errCatchUpDiverged = errors.New("this node holds restore points the writer does not")

// catchUpDivergedError 带上本机自写的快照，分叉处理要用它们点名和保留。
type catchUpDivergedError struct{ own []string }

func (e catchUpDivergedError) Error() string {
	return fmt.Sprintf("%v（例如 %s）", errCatchUpDiverged, e.own[0])
}
func (e catchUpDivergedError) Unwrap() error { return errCatchUpDiverged }

type catchUpStep struct {
	Kind   string // incremental | clone | full | destroy
	Rel    string // 相对目录根的数据集（"" 为根）
	From   string // incremental 的起点快照
	Origin string // clone 的源快照，相对路径（"/img@0"）
	To     string // 本步结束时的快照
}

func (s catchUpStep) String() string {
	name := s.Rel
	if name == "" {
		name = "(根)"
	}
	switch s.Kind {
	case "incremental":
		return fmt.Sprintf("incremental %s %s→%s", name, s.From, s.To)
	case "clone":
		return fmt.Sprintf("clone %s from %s to %s", name, s.Origin, s.To)
	case "full":
		return fmt.Sprintf("full %s to %s", name, s.To)
	default:
		return fmt.Sprintf("%s %s", s.Kind, name)
	}
}

type catalogueSnap struct {
	name    string
	guid    string
	created int64
}

type catalogueDataset struct {
	rel    string
	origin string // 相对的源快照；没有或在根外时为 ""
	snaps  []catalogueSnap
}

// catalogueDatasets 把清单按相对 root 的数据集分组，保持列表顺序（父在子前）。
func catalogueDatasets(inv zfs.GUIDInventory, root string) ([]string, map[string]*catalogueDataset) {
	var order []string
	byRel := map[string]*catalogueDataset{}
	for _, e := range inv.Entries {
		rel, ok := strings.CutPrefix(e.Name, root)
		if !ok || (rel != "" && !strings.HasPrefix(rel, "/") && !strings.HasPrefix(rel, "@")) {
			continue
		}
		if ds, snap, isSnap := strings.Cut(rel, "@"); isSnap {
			if d := byRel[ds]; d != nil {
				d.snaps = append(d.snaps, catalogueSnap{name: snap, guid: e.GUID, created: e.Created})
			}
			continue
		}
		d := &catalogueDataset{rel: rel}
		if o, ok := strings.CutPrefix(e.Origin, root); ok && strings.HasPrefix(o, "/") {
			d.origin = o
		}
		byRel[rel] = d
		order = append(order, rel)
	}
	return order, byRel
}

func (d *catalogueDataset) snap(name string) *catalogueSnap {
	for i := range d.snaps {
		if d.snaps[i].name == name {
			return &d.snaps[i]
		}
	}
	return nil
}

func (d *catalogueDataset) hasGUID(guid string) bool {
	for _, s := range d.snaps {
		if s.guid == guid {
			return true
		}
	}
	return false
}

// latestCommon 返回 sender 快照中本机也有的最新一个。
func latestCommon(sender, local *catalogueDataset) *catalogueSnap {
	for i := len(sender.snaps) - 1; i >= 0; i-- {
		if local.hasGUID(sender.snaps[i].guid) {
			return &sender.snaps[i]
		}
	}
	return nil
}

// writtenHereIn 列出本机在共同基准（baseMade）之后自己打、而发送方没有的快照，
// 即回滚会丢的内容；系统标记不算。
func writtenHereIn(local, sender *catalogueDataset, baseMade int64) []string {
	var out []string
	for _, s := range local.snaps {
		if storage.SystemSnapshot(s.name) || (sender != nil && sender.hasGUID(s.guid)) {
			continue
		}
		if baseMade > 0 && s.created > baseMade {
			out = append(out, local.rel+"@"+s.name)
		}
	}
	return out
}

// planCatchUp 逐数据集列出把本机追到 target 的步骤；base 是双方共有的根标记，baseMade 是它的创建时间。
func planCatchUp(sender zfs.GUIDInventory, senderRoot string, local zfs.GUIDInventory, localRoot, base, target string, baseMade int64) ([]catchUpStep, error) {
	sOrder, s := catalogueDatasets(sender, senderRoot)
	lOrder, l := catalogueDatasets(local, localRoot)
	var plan []catchUpStep

	// 写入者删掉又建了同名的镜像/配置：本机那份与对端没有共同快照，却是这一轮唯一过不去的地方。
	// 只要它带着共同的根标记（那一轮它就是写入者的数据集）、之后本机没有自己写过，它就只是
	// 写入者已经丢弃的旧数据集：删掉它（连同依赖它的克隆和子数据集）按新数据集重收，而不是整份重建。
	replaced, err := planReplacements(s, l, lOrder, base, target, baseMade)
	if err != nil {
		return nil, err
	}
	for _, rel := range replaced {
		plan = append(plan, catchUpStep{Kind: "destroy", Rel: rel})
		delete(l, rel)
	}

	// 执行完已列步骤后本机会存在的数据集。
	present := map[string]bool{}
	for rel := range l {
		present[rel] = true
	}
	parentOf := func(rel string) string {
		if i := strings.LastIndex(rel, "/"); i > 0 {
			return rel[:i]
		}
		return ""
	}
	// 没有目标标记的数据集是本轮之后才建的，留到下一轮。
	var pending []string
	for _, rel := range sOrder {
		if rel != "" && s[rel].snap(target) != nil {
			pending = append(pending, rel)
		}
	}
	for len(pending) > 0 {
		progressed := false
		var later []string
		for _, rel := range pending {
			d := s[rel]
			originRel, _, _ := strings.Cut(d.origin, "@")
			if _, have := l[rel]; !have && (!present[parentOf(rel)] || (originRel != "" && s[originRel] != nil && !present[originRel])) {
				later = append(later, rel) // 父数据集或克隆的源还没到
				continue
			}
			progressed = true
			if ld, have := l[rel]; have && len(ld.snaps) == 0 {
				// 没有快照的空壳（如重启时新建的库副本数据集）没有可保留的内容，删掉重收。
				plan = append(plan, catchUpStep{Kind: "destroy", Rel: rel})
			} else if have {
				common := latestCommon(d, ld)
				if common == nil {
					return nil, fmt.Errorf("%s 与对端没有共同快照", rel)
				}
				if own := writtenHereIn(ld, d, baseMade); len(own) > 0 {
					return nil, catchUpDivergedError{own: own}
				}
				if common.name != target {
					plan = append(plan, catchUpStep{Kind: "incremental", Rel: rel, From: common.name, To: target})
				}
				continue
			}
			first := d.snaps[0].name
			if d.origin != "" && originHeld(d.origin, s, l, present) {
				plan = append(plan, catchUpStep{Kind: "clone", Rel: rel, Origin: d.origin, To: first})
			} else {
				plan = append(plan, catchUpStep{Kind: "full", Rel: rel, To: first})
			}
			if first != target {
				plan = append(plan, catchUpStep{Kind: "incremental", Rel: rel, From: first, To: target})
			}
			present[rel] = true
		}
		if !progressed {
			return nil, fmt.Errorf("找不到这些数据集的父数据集或克隆源：%s", strings.Join(later, "、"))
		}
		pending = later
	}

	var gone []string
	for _, rel := range lOrder {
		if rel == "" || s[rel] != nil || l[rel] == nil {
			continue
		}
		if own := writtenHereIn(l[rel], nil, baseMade); len(own) > 0 {
			return nil, catchUpDivergedError{own: own}
		}
		gone = append(gone, rel)
	}
	sort.SliceStable(gone, func(i, j int) bool { return strings.Count(gone[i], "/") > strings.Count(gone[j], "/") })
	for _, rel := range gone {
		plan = append(plan, catchUpStep{Kind: "destroy", Rel: rel})
	}

	// 根自己的标记放最后：根收到了目标标记，这一轮才算完整。
	sr, lr := s[""], l[""]
	if sr == nil || lr == nil || sr.snap(target) == nil {
		return nil, fmt.Errorf("目录根缺少标记 %s", target)
	}
	common := latestCommon(sr, lr)
	if common == nil {
		return nil, fmt.Errorf("目录根与对端没有共同快照")
	}
	if common.name != target {
		plan = append(plan, catchUpStep{Kind: "incremental", Rel: "", From: common.name, To: target})
	}
	return plan, nil
}

// recreatedUnderSameName 判断本轮是否有要按 planReplacements 删掉重收的数据集（写入者删了又建的同名数据集）。
func recreatedUnderSameName(peerInv ha.ReplicationInventory, local zfs.GUIDInventory, localRoot, base, target string) bool {
	var baseMade int64
	for _, e := range local.Entries {
		if e.Name == localRoot+"@"+base {
			baseMade = e.Created
		}
	}
	_, s := catalogueDatasets(zfs.GUIDInventory{Entries: peerInv.Entries}, peerInv.Root)
	lOrder, l := catalogueDatasets(local, localRoot)
	replaced, err := planReplacements(s, l, lOrder, base, target, baseMade)
	return err == nil && len(replaced) > 0
}

// planReplacements 返回要先删掉、再按新数据集重收的本机数据集，依赖者在前（克隆先于源、子先于父）。
// 只认 planCatchUp 说明的那一种：没有共同快照、带着共同根标记 base、base 之后本机没写过；
// 依赖它的本机数据集一并重收，其中本机写过的照样按分叉处理。不满足的留给原来的报错（整份重建）。
func planReplacements(s, l map[string]*catalogueDataset, lOrder []string, base, target string, baseMade int64) ([]string, error) {
	if base == "" || baseMade <= 0 {
		return nil, nil
	}
	set := map[string]bool{}
	for _, rel := range lOrder {
		d, ld := s[rel], l[rel]
		if rel == "" || d == nil || ld == nil || len(ld.snaps) == 0 || d.snap(target) == nil || latestCommon(d, ld) != nil {
			continue
		}
		if ld.snap(base) == nil {
			continue
		}
		if own := writtenHereIn(ld, d, baseMade); len(own) > 0 {
			return nil, catchUpDivergedError{own: own}
		}
		set[rel] = true
	}
	if len(set) == 0 {
		return nil, nil
	}
	dependsOn := func(rel, on string) bool {
		if strings.HasPrefix(rel, on+"/") {
			return true
		}
		originRel, _, _ := strings.Cut(l[rel].origin, "@")
		return originRel == on
	}
	for grown := true; grown; {
		grown = false
		for _, rel := range lOrder {
			if rel == "" || set[rel] {
				continue
			}
			for on := range set {
				if dependsOn(rel, on) {
					if own := writtenHereIn(l[rel], s[rel], baseMade); len(own) > 0 {
						return nil, catchUpDivergedError{own: own}
					}
					set[rel], grown = true, true
					break
				}
			}
		}
	}
	var out []string
	for len(set) > 0 {
		var ready []string
		for rel := range set {
			blocked := false
			for other := range set {
				if other != rel && dependsOn(other, rel) {
					blocked = true
					break
				}
			}
			if !blocked {
				ready = append(ready, rel)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("要重收的数据集互相依赖，无法排出删除顺序")
		}
		sort.Strings(ready)
		for _, rel := range ready {
			out = append(out, rel)
			delete(set, rel)
		}
	}
	return out, nil
}

// originHeld 判断克隆源（含发送方的那个快照）本机已有，或会由前面的步骤建出。
func originHeld(origin string, s, l map[string]*catalogueDataset, present map[string]bool) bool {
	rel, snap, ok := strings.Cut(origin, "@")
	if !ok || !present[rel] {
		return false
	}
	sd := s[rel]
	if sd == nil {
		return false
	}
	want := sd.snap(snap)
	if want == nil {
		return false
	}
	if ld := l[rel]; ld != nil {
		return ld.hasGUID(want.guid)
	}
	return true // 本轮前面的步骤会把它连同这个快照一起建出来
}

// errCatchUpInterrupted 标记追平因传输中断而失败（取不到流、流中途断开），下一轮重试即可，不必整体重建。
var errCatchUpInterrupted = errors.New("追平被传输中断")

// catchUp 在递归轮被拒后逐数据集追到 target。被拒的接收可能已推进部分数据集
// 并留下续传状态，所以先清续传再重新读本机清单。
func (r *Replicator) catchUp(ctx context.Context, peerInv ha.ReplicationInventory, base, target string) error {
	if dataset, token, err := r.ZFS.ResumeToken(ctx, r.Root); err == nil && token != "" {
		if err := r.ZFS.AbortResume(ctx, dataset); err != nil {
			return err
		}
	}
	local, err := r.ZFS.ListGUIDs(ctx, r.Root)
	if err != nil {
		return err
	}
	var baseMade int64
	for _, e := range local.Entries {
		if e.Name == r.Root+"@"+base {
			baseMade = e.Created
		}
	}
	plan, err := planCatchUp(zfs.GUIDInventory{Entries: peerInv.Entries}, peerInv.Root, local, r.Root, base, target, baseMade)
	if err != nil {
		return err
	}
	for _, step := range plan {
		if step.Kind == "destroy" {
			// 本机客户机的盘还克隆自它时挪到目录外暂存，不让一台客户机挡住整轮（见 destroyOrHold）。
			if _, err := r.destroyOrHold(ctx, r.Root+step.Rel); err != nil {
				return fmt.Errorf("%s: %w", step, err)
			}
			continue
		}
		stream, err := r.Peer.StreamDataset(ctx, step.Rel, step.From, step.Origin, step.To)
		if err != nil {
			return fmt.Errorf("%s: %w: %w", step, errCatchUpInterrupted, err)
		}
		err = r.ZFS.ReceiveDataset(ctx, r.Root+step.Rel, stream)
		stream.Close()
		if err != nil {
			if zfs.IsStreamBroken(err) {
				return fmt.Errorf("%s: %w: %w", step, errCatchUpInterrupted, err)
			}
			return fmt.Errorf("%s: %w", step, err)
		}
	}
	return nil
}

// CompleteMarker 返回收来的副本完整持有的最新一轮：根的最新标记，且根下每个
// 数据集都有它；否则返回 ""。递归流先落根，中途失败的一轮不能拿来接任。
// 仅用于接收方；写入者自己的副本用 WriterMarker（递归快照是原子的）。不能用
// creation 区分两者：接收端的 creation 是收到的时间。
func CompleteMarker(inv zfs.GUIDInventory, root string) string {
	newest := strings.TrimPrefix(latestMarker(root, inv), root+"@")
	if newest == "" {
		return ""
	}
	order, byRel := catalogueDatasets(inv, root)
	for _, rel := range order {
		if byRel[rel].snap(newest) == nil {
			return ""
		}
	}
	return newest
}

// WriterMarker 返回写入者自己的最新一轮（见 CompleteMarker）。
func WriterMarker(inv zfs.GUIDInventory, root string) string {
	return strings.TrimPrefix(latestMarker(root, inv), root+"@")
}
