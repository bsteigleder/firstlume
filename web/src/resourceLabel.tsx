import type { ResourceLabel } from "./api";

// A resource's name line: its name, and for a DNS record its type,
// target and proxy; for other kinds the identifying details.
export function LabelLine({ label, data }: { label: ResourceLabel; data: boolean }) {
  const rec = label.record;
  return (
    <span className="res-label">
      <strong className={data ? "muted" : undefined}>{label.name}</strong>
      {rec?.type && <span className="res-rtype">{rec.type}</span>}
      {rec?.target && <span className="mono res-target">→ {rec.target}</span>}
      {rec?.proxied && <span className="res-proxied">proxied</span>}
      {!rec && label.details?.length ? (
        <span className="muted small">{label.details.map((d) => `${d.key} ${d.value}`).join(" · ")}</span>
      ) : null}
    </span>
  );
}
