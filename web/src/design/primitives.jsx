// Shared primitives, each used by at least two components.
import React from "react";
import { createPortal } from "react-dom";
import { Icons } from "./icons.jsx";
import { PAGE_SIZES } from "../lib/hooks.js";

// Form fields place labels above inputs and optionally show required markers and hints. Help is hidden behind a ? button
// for information needed only occasionally. helpInline positions it by the control; helpAfterLabel follows the label.
function Field({
  label,
  required,
  hint,
  help,
  helpInline,
  helpAfterLabel,
  children,
}) {
  const [showHelp, setShowHelp] = React.useState(false);
  const helpText =
    help && showHelp ? (
      <div className="hint" style={{ margin: 0 }}>
        {help}
      </div>
    ) : null;
  return (
    <div style={{ marginBottom: 14 }}>
      <div
        style={{
          fontSize: 12,
          color: "var(--fg-mute)",
          marginBottom: 6,
          fontWeight: 500,
        }}
      >
        {label} {required && <span style={{ color: "var(--rose)" }}>*</span>}
        {help && (
          <button
            type="button"
            className="field-help"
            aria-label={`Help for ${label}`}
            aria-expanded={showHelp}
            onClick={() => setShowHelp((v) => !v)}
          >
            ?
          </button>
        )}
        {helpAfterLabel && help && showHelp && (
          <span className="hint" style={{ marginLeft: 8, fontWeight: 400 }}>
            {help}
          </span>
        )}
      </div>
      {helpInline ? (
        <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
          {children}
          {helpText}
        </div>
      ) : (
        children
      )}
      {hint && (
        <div className="hint" style={{ marginTop: 4 }}>
          {hint}
        </div>
      )}
      {!helpInline && !helpAfterLabel && help && showHelp && (
        <div className="hint" style={{ marginTop: 4 }}>
          {help}
        </div>
      )}
    </div>
  );
}

function Select({ value, onChange, options, ...rest }) {
  return (
    <select
      className="input"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      style={{ width: "100%" }}
      {...rest}
    >
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  );
}

// Shared table placeholder row for loading, error, or empty results; otherwise an empty filter looks broken.
function TableState({ colSpan, loading, error, empty, hint = "No data" }) {
  if (!loading && !error && !empty) return null;
  return (
    <tr>
      <td
        colSpan={colSpan}
        style={{
          textAlign: "center",
          padding: "32px 14px",
          fontSize: 13,
          color: error ? "var(--rose)" : "var(--fg-faint)",
        }}
      >
        {loading ? "Loading…" : error ? `Load failed: ${error}` : hint}
      </td>
    </tr>
  );
}

// Search input with leading icon and Enter submission for backend-filtered lists.
function SearchBox({ value, onChange, placeholder, onSubmit, width = 240 }) {
  return (
    <div className="search-box" style={{ width }}>
      <Icons.Search
        size={12}
        style={{ color: "var(--fg-faint)", flexShrink: 0 }}
      />
      <input
        value={value}
        placeholder={placeholder}
        aria-label={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (onSubmit && e.key === "Enter") onSubmit();
        }}
      />
    </div>
  );
}

// Pagination with totals and page sizes; hide entirely if everything fits on the smallest page.
function Pager({ page, size, total, onPage, onSize, sizes = PAGE_SIZES }) {
  if (total <= sizes[0]) return null;
  const pages = Math.max(1, Math.ceil(total / size));
  return (
    <div className="pager">
      <span className="meta" style={{ marginRight: "auto" }}>
        Total {total} entries
      </span>
      <Select
        aria-label="Rows per page"
        value={String(size)}
        onChange={(v) => onSize(Number(v))}
        options={sizes.map((n) => ({
          value: String(n),
          label: `${n} per page`,
        }))}
        style={{ width: 96 }}
      />
      <button
        className="btn"
        disabled={page <= 1}
        onClick={() => onPage(page - 1)}
      >
        Previous
      </button>
      <span className="mono" style={{ fontSize: 13, color: "var(--fg-mute)" }}>
        {page} / {pages}
      </span>
      <button
        className="btn"
        disabled={page >= pages}
        onClick={() => onPage(page + 1)}
      >
        Next
      </button>
    </div>
  );
}

// Segmented filters can include counts in each segment; options are [{ id, label, n?, color? }].
function SegFilter({ value, onChange, options }) {
  return (
    <div className="seg">
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          className={value === o.id ? "on" : ""}
          onClick={() => onChange(o.id)}
          style={
            value === o.id && o.color
              ? {
                  color: `var(--${o.color})`,
                  boxShadow: `0 0 0 1px var(--${o.color}) inset`,
                  background: `var(--${o.color}-soft)`,
                }
              : null
          }
        >
          {o.label}
          {o.n != null && <span className="n">{o.n}</span>}
        </button>
      ))}
    </div>
  );
}

// Statistics card: pct adds a usage bar; sub adds supporting metadata.
function StatCard({ label, value, unit, sub, accent = "cyan", pct }) {
  return (
    <div className="card stat-card">
      <div className="lbl">{label}</div>
      <div
        style={{
          display: "flex",
          alignItems: "baseline",
          gap: 5,
          marginTop: 5,
        }}
      >
        <span className="stat-num" style={{ color: `var(--${accent})` }}>
          {value}
        </span>
        {unit && (
          <span style={{ fontSize: 13, color: "var(--fg-mute)" }}>{unit}</span>
        )}
      </div>
      {sub && (
        <div className="mono meta" style={{ marginTop: 7 }}>
          {sub}
        </div>
      )}
      {pct != null && (
        <div
          className={`bar ${pct > 85 ? "rose" : pct > 70 ? "amber" : ""}`}
          style={{ marginTop: 8 }}
        >
          <span style={{ width: `${pct}%` }} />
        </div>
      )}
      <div
        className="stat-card-orb"
        style={{ background: `var(--${accent}-soft)` }}
      />
    </div>
  );
}

// Render toggles as controls instead of buttons that alternate text; labels should communicate state, not action.
function Toggle({
  checked,
  onChange,
  onLabel = "Enabled",
  offLabel = "Disabled",
}) {
  return (
    <div className="seg" style={{ width: "fit-content" }}>
      <button
        type="button"
        className={!checked ? "on" : ""}
        onClick={() => onChange(false)}
      >
        {offLabel}
      </button>
      <button
        type="button"
        className={checked ? "on" : ""}
        onClick={() => onChange(true)}
      >
        {onLabel}
      </button>
    </div>
  );
}

// Tip displays contextual help on click; native title hover is too slow and not visibly interactive.
// Dismiss via a transparent backdrop, like notifications; clicking elsewhere closes it.
function Tip({ text, label = "Help" }) {
  const [box, setBox] = React.useState(null); // Viewport coordinates while expanded
  React.useEffect(() => {
    if (!box) return undefined;
    const close = () => setBox(null);
    const onKey = (e) => {
      if (e.key === "Escape") close();
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("scroll", close, true); // Fixed overlays can lose alignment when scrolling
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", close, true);
    };
  }, [box]);
  const toggle = (e) => {
    e.stopPropagation();
    if (box) {
      setBox(null);
      return;
    }
    // Portal to body using viewport coordinates, avoiding table clipping; reserve 16px on narrow screens.
    const r = e.currentTarget.getBoundingClientRect();
    const vw = document.documentElement.clientWidth || window.innerWidth;
    const width = Math.min(320, vw - 32);
    const left = Math.max(16, Math.min(r.left - 8, vw - 16 - width));
    setBox({ left, top: r.bottom + 6, width });
  };
  return (
    <span style={{ display: "inline-flex" }}>
      <button
        type="button"
        aria-label={label}
        className="field-help"
        aria-expanded={!!box}
        onClick={toggle}
      >
        ?
      </button>
      {box &&
        createPortal(
          <>
            <span
              data-testid="tip-backdrop"
              onClick={() => setBox(null)}
              style={{
                position: "fixed",
                inset: 0,
                zIndex: "var(--z-popover)",
              }}
            />
            <span
              className="card"
              style={{
                position: "fixed",
                top: box.top,
                left: box.left,
                zIndex: "var(--z-popover)",
                width: box.width,
                padding: "10px 12px",
                fontSize: 13,
                lineHeight: 1.7,
                color: "var(--fg-soft)",
                fontWeight: 400,
                whiteSpace: "normal",
                boxShadow: "0 8px 24px oklch(0 0 0 / 0.45)",
              }}
            >
              {text}
            </span>
          </>,
          document.body,
        )}
    </span>
  );
}

export {
  Field,
  Select,
  TableState,
  SearchBox,
  Pager,
  SegFilter,
  StatCard,
  Toggle,
  Tip,
};
