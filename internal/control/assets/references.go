package assets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/place"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/zfs"
	"github.com/tianwei/diskless/internal/store"
)

// referencedBy 判断 list 中是否有元素经 key 引用 id，供各类「正在使用」检查共用。
func referencedBy[T any](list []T, id string, key func(T) string) bool {
	for _, item := range list {
		if key(item) == id {
			return true
		}
	}
	return false
}

// EnsureSuperAvailable 在另一台超管机已占用同一配置时拒绝启用超管，否则两台的保存会互相覆盖。
// 只读数据库，TerminalService（新建/更新）和 BootService（开机）直接调用。
func EnsureSuperAvailable(ctx context.Context, st store.Store, target domain.Terminal) error {
	if err := ensureNoInstallation(ctx, st, target.MAC); err != nil {
		return err
	}
	groups, err := st.Groups().List(ctx)
	if err != nil {
		return err
	}
	groupsByID := make(map[string]domain.Group, len(groups))
	for _, group := range groups {
		groupsByID[group.ID] = group
	}
	targetGroup, ok := groupsByID[target.GroupID]
	if !ok {
		return errs.NotFound("分组 " + target.GroupID + " 不存在")
	}
	disks, err := st.GroupDisks().List(ctx)
	if err != nil {
		return err
	}
	// 超管机开着期间占住其分组启动用的全部配置（系统盘和各数据盘），关机保存可能覆盖其中任一个；
	// 不论在哪个分组，两台占同一配置都会互相覆盖。
	held := groupConfigIDs(targetGroup, disks)
	terminals, err := st.Terminals().List(ctx)
	if err != nil {
		return err
	}
	alreadySuper := false
	for _, terminal := range terminals {
		if terminal.ID == target.ID && terminal.IsSuper {
			alreadySuper = true
			break
		}
	}
	// 已启用的超管开机仍可读取盘；只拦目录任务进行期间新启用的模式。
	if !alreadySuper {
		if err := refuseSuperDuringConfigChange(ctx, st, held); err != nil {
			return err
		}
	}
	for _, terminal := range terminals {
		if !terminal.IsSuper || terminal.ID == target.ID {
			continue
		}
		group, ok := groupsByID[terminal.GroupID]
		if !ok {
			continue
		}
		for configID := range groupConfigIDs(group, disks) {
			if held[configID] {
				return TerminalSuperConflict{ConfigID: configID, TerminalID: terminal.ID, MAC: terminal.MAC}
			}
		}
	}
	return nil
}

// SuperEditingConfig 返回当前占用该配置的超管机（若有）。占用范围见 EnsureSuperAvailable。
func SuperEditingConfig(ctx context.Context, st store.Store, configID string) (domain.Terminal, bool, error) {
	if configID == "" {
		return domain.Terminal{}, false, nil
	}
	held, err := superHeldConfigs(ctx, st)
	if err != nil {
		return domain.Terminal{}, false, err
	}
	terminal, ok := held[configID]
	return terminal, ok, nil
}

// superHeldConfigs 一次读表算出各超管机占用的配置（所在分组的系统盘和各数据盘）及占用者。
func superHeldConfigs(ctx context.Context, st store.Store) (map[string]domain.Terminal, error) {
	terminals, err := st.Terminals().List(ctx)
	if err != nil {
		return nil, err
	}
	var supers []domain.Terminal
	for _, terminal := range terminals {
		if terminal.IsSuper {
			supers = append(supers, terminal)
		}
	}
	if len(supers) == 0 {
		return nil, nil
	}
	groups, err := st.Groups().List(ctx)
	if err != nil {
		return nil, err
	}
	groupsByID := make(map[string]domain.Group, len(groups))
	for _, group := range groups {
		groupsByID[group.ID] = group
	}
	disks, err := st.GroupDisks().List(ctx)
	if err != nil {
		return nil, err
	}
	held := map[string]domain.Terminal{}
	for _, terminal := range supers {
		group, ok := groupsByID[terminal.GroupID]
		if !ok {
			continue
		}
		for configID := range groupConfigIDs(group, disks) {
			if _, taken := held[configID]; !taken {
				held[configID] = terminal
			}
		}
	}
	return held, nil
}

func superModeRefusal(terminal domain.Terminal, action string) error {
	who := strings.TrimSpace(terminal.Name)
	if who == "" {
		who = formatMAC(terminal.MAC)
	}
	return errs.Conflict(fmt.Sprintf("请先取消超管机「%s」的超管，再%s（要保留改动，先关机后点「关机后存还原点」）", who, action))
}

func refuseSuperGroupChange(ctx context.Context, st store.Store, groupID, action string) error {
	terminals, err := st.Terminals().List(ctx)
	if err != nil {
		return err
	}
	for _, terminal := range terminals {
		if terminal.IsSuper && terminal.GroupID == groupID {
			return superModeRefusal(terminal, action)
		}
	}
	return nil
}

func refuseSuperConfigChange(ctx context.Context, st store.Store, configID, action string) error {
	terminal, editing, err := SuperEditingConfig(ctx, st, configID)
	if err != nil {
		return err
	}
	if editing {
		return superModeRefusal(terminal, action)
	}
	return nil
}

// 合并、覆盖和删除镜像会影响其全部配置；另存配置只读来源，不走此检查。
func refuseSuperImageChange(ctx context.Context, st store.Store, imageID, action string) error {
	held, err := superHeldConfigs(ctx, st)
	if err != nil || len(held) == 0 {
		return err
	}
	configs, err := st.Configs().ListByImage(ctx, imageID)
	if err != nil {
		return err
	}
	for _, cfg := range configs {
		if terminal, ok := held[cfg.ID]; ok {
			return superModeRefusal(terminal, action)
		}
	}
	return nil
}

// groupConfigIDs 返回分组启动用的配置：系统配置在前，再按顺序接各数据盘配置。
// disks 是整张 group_disks 表，只取本分组的行。
func groupConfigIDs(group domain.Group, disks []domain.GroupDisk) map[string]bool {
	ids := map[string]bool{}
	if group.SystemConfigID != "" {
		ids[group.SystemConfigID] = true
	}
	for _, disk := range disks {
		if disk.GroupID == group.ID && disk.ConfigID != "" {
			ids[disk.ConfigID] = true
		}
	}
	return ids
}

// refIndex 基于一次读取的分组、数据盘、克隆快照回答删除守卫「X 是否仍被引用」；
// 各实体的引用规则集中在这里，新增引用来源只改一处。
type refIndex struct {
	groups []domain.Group
	disks  []domain.GroupDisk
	clones []domain.ClientClone
}

func loadRefIndex(ctx context.Context, st store.Store) (refIndex, error) {
	groups, err := st.Groups().List(ctx)
	if err != nil {
		return refIndex{}, err
	}
	disks, err := st.GroupDisks().List(ctx)
	if err != nil {
		return refIndex{}, err
	}
	clones, err := st.ClientClones().List(ctx)
	if err != nil {
		return refIndex{}, err
	}
	return refIndex{groups: groups, disks: disks, clones: clones}, nil
}

func (ix refIndex) configInUse(id string) bool {
	return referencedBy(ix.groups, id, func(g domain.Group) string { return g.SystemConfigID }) ||
		referencedBy(ix.disks, id, func(d domain.GroupDisk) string { return d.ConfigID }) ||
		referencedBy(ix.clones, id, func(c domain.ClientClone) string { return c.ConfigID })
}

func (ix refIndex) reductionInUse(id string) bool {
	return referencedBy(ix.groups, id, func(g domain.Group) string { return g.SystemReductionID }) ||
		referencedBy(ix.clones, id, func(c domain.ClientClone) string { return c.ReductionID })
}

// groupsUsingConfig 列出启动该配置的分组。合并保留配置但并掉还原点，这些分组改指新基线而不是拒绝合并。
func (ix refIndex) groupsUsingConfig(id string) []domain.Group {
	var out []domain.Group
	for _, group := range ix.groups {
		if group.SystemConfigID == id {
			out = append(out, group)
		}
	}
	return out
}

// clonesUsingConfig 列出基于该配置运行的客户机克隆，即盘克隆自将被销毁快照的机器。
func (ix refIndex) clonesUsingConfig(id string) []domain.ClientClone {
	var out []domain.ClientClone
	for _, clone := range ix.clones {
		if clone.ConfigID == id {
			out = append(out, clone)
		}
	}
	return out
}

// describeConfigUsers 点名仍绑定该配置的对象，供操作者照着先解绑。
func (ix refIndex) describeConfigUsers(id string) []string {
	groupNames := make(map[string]string, len(ix.groups))
	for _, group := range ix.groups {
		groupNames[group.ID] = group.Name
	}
	var users []string
	for _, group := range ix.groupsUsingConfig(id) {
		users = append(users, "分组 "+group.Name)
	}
	for _, disk := range ix.disks {
		if disk.ConfigID != id {
			continue
		}
		name := groupNames[disk.GroupID]
		if name == "" {
			name = disk.GroupID
		}
		users = append(users, "分组 "+name+" 的数据盘 "+disk.MountTarget)
	}
	for _, clone := range ix.clonesUsingConfig(id) {
		users = append(users, "客户机 "+formatMAC(clone.TerminalMAC))
	}
	return users
}

// describeImageUsers 点名仍绑定该镜像的对象，措辞与 describeConfigUsers 一致。
func (ix refIndex) describeImageUsers(id string, configIDs, reductionIDs []string) []string {
	groupNames := make(map[string]string, len(ix.groups))
	for _, group := range ix.groups {
		groupNames[group.ID] = group.Name
	}
	var users []string
	for _, group := range ix.groups {
		if group.SystemImageID == id {
			users = append(users, "分组 "+group.Name)
		}
	}
	for _, disk := range ix.disks {
		if disk.ImageID != id {
			continue
		}
		name := groupNames[disk.GroupID]
		if name == "" {
			name = disk.GroupID
		}
		users = append(users, "分组 "+name+" 的数据盘 "+disk.MountTarget)
	}
	cfgSet := make(map[string]bool, len(configIDs))
	for _, cfg := range configIDs {
		cfgSet[cfg] = true
	}
	redSet := make(map[string]bool, len(reductionIDs))
	for _, red := range reductionIDs {
		redSet[red] = true
	}
	for _, clone := range ix.clones {
		if cfgSet[clone.ConfigID] || redSet[clone.ReductionID] {
			users = append(users, "客户机 "+formatMAC(clone.TerminalMAC))
		}
	}
	return dedupe(users)
}

// blocked 把哨兵错误（供调用方和传输层分类）与面向操作者的文案配对。
// 不用 %w 包装，是因为那会给每条拒绝都加上哨兵自己的措辞。
type blockedError struct {
	sentinel error
	msg      string
}

func (e blockedError) Error() string { return e.msg }
func (e blockedError) Is(target error) bool {
	return target == e.sentinel || errors.Is(e.sentinel, target)
}

func blocked(sentinel error, msg string) error {
	if msg == "" {
		return sentinel
	}
	return blockedError{sentinel: sentinel, msg: msg}
}

// explainBlockers 把占用数据集翻译成操作指引。阻塞种类不同（健康检查会自己结束、fork 配置要删、
// 运行中的机器要关），每类各用一句，不能笼统写成「请删除这些」。subject 是被阻塞的对象，如「该配置」。
func explainBlockers(ctx context.Context, st store.Store, datasets []string, subject string) string {
	var inspecting, exporting bool
	var configs, clients, supers, superModes, identities, others []string
	for _, dataset := range datasets {
		carrier := storage.Classify(storage.DatasetID(dataset))
		switch carrier.Kind {
		case storage.CarrierInspect:
			inspecting = true
		case storage.CarrierExport:
			exporting = true
		case storage.CarrierSuper:
			supers = append(supers, formatMAC(carrier.MAC))
		case storage.CarrierAdapt:
			others = append(others, "客户机 "+formatMAC(carrier.MAC)+" 的机型适配盘")
		case storage.CarrierIdentity:
			// 能清的身份层已由 splitIdentityCaches 挑走；超管机的不能清，清理会连带超管盘。
			if t, err := st.Terminals().GetByMAC(ctx, carrier.MAC); err == nil && t.IsSuper {
				superModes = append(superModes, formatMAC(carrier.MAC))
			} else {
				identities = append(identities, formatMAC(carrier.MAC))
			}
		case storage.CarrierClient:
			clients = append(clients, formatMAC(carrier.MAC))
		default:
			if cfg, err := st.Configs().Get(ctx, carrier.Name); err == nil {
				configs = append(configs, cfg.Name)
			} else {
				others = append(others, carrier.Name)
			}
		}
	}
	// 每行先写该做什么再列对象，操作者不必理解池的拓扑。
	var parts []string
	if inspecting {
		parts = append(parts, "镜像体检正在进行，请稍后重试")
	}
	if exporting {
		parts = append(parts, "还原点正在导出或另存为新镜像，请等任务结束后重试")
	}
	if len(configs) > 0 {
		parts = append(parts, fmt.Sprintf("请先删除由%s派生出的配置：%s", subject, strings.Join(dedupe(configs), "、")))
	}
	if len(clients) > 0 {
		parts = append(parts, "请先关闭正在使用的客户机："+strings.Join(dedupe(clients), "、"))
	}
	if len(supers) > 0 {
		parts = append(parts, "请先关闭正在使用的超管机："+strings.Join(dedupe(supers), "、"))
	}
	if len(identities) > 0 {
		parts = append(parts, "请先关闭这些客户机，关机满 5 分钟后重试："+strings.Join(dedupe(identities), "、"))
	}
	if len(superModes) > 0 {
		parts = append(parts, "请先结束超管机的超管模式（保存或放弃）："+strings.Join(dedupe(superModes), "、"))
	}
	if len(others) > 0 {
		parts = append(parts, "请先清理仍在占用的对象："+strings.Join(dedupe(others), "、"))
	}
	return strings.Join(parts, "；")
}

// splitIdentityCaches 从占用数据集里分出关机客户机的身份层：身份层是开机时按还原点生成的缓存，
// 删掉后下次开机重建，不该挡住删除。可能还在用的机器留在 rest 里照常拦。
func splitIdentityCaches(ctx context.Context, st store.Store, agent storage.StorageAgent, datasets []string) (macs, rest []string, err error) {
	if _, ok := agent.(storage.IdentityHostAgent); !ok {
		return nil, datasets, nil
	}
	for _, dataset := range datasets {
		carrier := storage.Classify(storage.DatasetID(dataset))
		if carrier.Kind != storage.CarrierIdentity {
			rest = append(rest, dataset)
			continue
		}
		idle, err := terminalIdle(ctx, st, carrier.MAC)
		if err != nil {
			return nil, nil, err
		}
		if idle {
			macs = append(macs, carrier.MAC)
		} else {
			rest = append(rest, dataset)
		}
	}
	return macs, rest, nil
}

// terminalIdle 判断能否清这台机器的身份层。清理会拆它的克隆，所以与离线回收守同一条规则：
// 普通机、关机满 cloneGracePeriod、其间没开过机、没有 WinPE 任务。没连上过的机器没有关机时间，只看开机；
// 库里没有这台机器时它不可能在用这层。
func terminalIdle(ctx context.Context, st store.Store, mac string) (bool, error) {
	t, err := st.Terminals().GetByMAC(ctx, mac)
	if errs.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	now := time.Now()
	if t.IsSuper || t.State != domain.TerminalStateOffline {
		return false, nil
	}
	if t.OfflineAt != nil && now.Sub(*t.OfflineAt) < cloneGracePeriod {
		return false, nil
	}
	if t.LastBootAt != nil && now.Sub(*t.LastBootAt) < cloneGracePeriod {
		return false, nil
	}
	if _, err := st.PESessions().ActiveByTerminal(ctx, t.ID); err == nil {
		return false, nil
	} else if !errs.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

func reductionIDsOf(reductions []domain.Reduction) []string {
	ids := make([]string, 0, len(reductions))
	for _, r := range reductions {
		ids = append(ids, r.ID)
	}
	return ids
}

// nodeLayers 按身份层记录找出建自这些还原点的层，按所在节点分组。删除只查本机池，看不到建在别的节点上的层；
// 它们会钉住那台节点上的副本，复制删不掉，最后只能整份重建。机器还在用的拒绝并点名。
func nodeLayers(ctx context.Context, st store.Store, reductionIDs []string, sentinel error) (map[string][]string, error) {
	want := make(map[string]bool, len(reductionIDs))
	for _, id := range reductionIDs {
		want[id] = true
	}
	identities, err := st.TerminalIdentities().List(ctx)
	if err != nil {
		return nil, err
	}
	layers := map[string][]string{}
	var busy []string
	for _, v := range identities {
		if v.LayerServerID == "" || !want[v.LayerReductionRef] {
			continue
		}
		t, err := st.Terminals().Get(ctx, v.TerminalID)
		if errs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		idle, err := terminalIdle(ctx, st, t.MAC)
		if err != nil {
			return nil, err
		}
		mac := storage.NormalizeMAC(t.MAC)
		if !idle {
			busy = append(busy, formatMAC(mac))
			continue
		}
		red, err := st.Reductions().Get(ctx, v.LayerReductionRef)
		if err != nil {
			return nil, err
		}
		layers[v.LayerServerID] = append(layers[v.LayerServerID], red.ConfigID+"@"+strings.TrimPrefix(red.Name, "@"))
	}
	if len(busy) > 0 {
		return nil, blocked(sentinel, "请先关闭这些客户机，关机满 5 分钟后重试："+strings.Join(dedupe(busy), "、"))
	}
	return layers, nil
}

// clearNodeLayers resolves the exact recorded owners; unknown or unreachable owners must not silently fall back.
func clearNodeLayers(ctx context.Context, router *place.Router, layers map[string][]string) error {
	if len(layers) == 0 {
		return nil
	}
	if router == nil {
		return errs.Conflict("请先配置身份层所在节点的路由，再重试删除")
	}
	ids := make([]string, 0, len(layers))
	for id := range layers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	hosts, err := router.NamedHosts(ctx, ids)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		host, ok := h.Agent.(storage.IdentityLayerReleaser)
		if !ok {
			return storage.ErrNotImplemented
		}
		if _, err := host.ReleaseIdentityLayers(ctx, dedupe(layers[h.ServerID])); err != nil {
			return errs.Conflict(fmt.Sprintf("节点「%s」的身份层没有清掉，请确认客户机已关机后重试：%s", h.ServerID, zfs.OperatorMessage(err)))
		}
	}
	return nil
}

// clearIdentityCaches passes expected sources so a concurrent rebuild can never turn a stale MAC into an unconditional delete.
func clearIdentityCaches(ctx context.Context, agent storage.StorageAgent, macs []string, sources ...string) error {
	if len(macs) == 0 {
		return nil
	}
	host, ok := agent.(storage.IdentityLayerReleaser)
	if !ok {
		return storage.ErrNotImplemented
	}
	if _, err := host.ReleaseIdentityLayers(ctx, sources); err != nil {
		return errs.Conflict(fmt.Sprintf("身份层没有清掉，请确认客户机已关机后重试：%s", zfs.OperatorMessage(err)))
	}
	return nil
}

// formatMAC 给规范化的 MAC 加分隔符，与设备标签上的写法一致。
func formatMAC(mac string) string {
	if len(mac) != 12 {
		return mac
	}
	var b strings.Builder
	for i := 0; i < len(mac); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(mac[i : i+2])
	}
	return b.String()
}

func dedupe(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := items[:0]
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// filterDependents 去掉本来就会被销毁的数据集（合并的兄弟配置、镜像自己的配置），只报真正阻塞的。
func filterDependents(dependents []string, ignore map[string]bool) []string {
	var blockers []string
	for _, dataset := range dependents {
		if ignore[storage.DatasetID(dataset)] {
			continue
		}
		blockers = append(blockers, dataset)
	}
	return blockers
}

// clonesOfMAC 筛出某台客户机的克隆，供各条「清掉该机克隆」路径共用。
func clonesOfMAC(clones []domain.ClientClone, mac string) []domain.ClientClone {
	var out []domain.ClientClone
	for _, clone := range clones {
		if clone.TerminalMAC == mac {
			out = append(out, clone)
		}
	}
	return out
}
