import { useState } from "react";
import { Link } from "react-router";
import type { ChangeAction, ResourceLabel, ResourceUnit, Stack, StackResources as Resources, StateResource } from "../api";
import { useOrg } from "../org";
import { providerColor, providerName, ResourceIcon, resourceKind } from "../resourceIcons";
import { LabelLine } from "../resourceLabel";
import { Empty, ErrorBanner, timeAgo, useData } from "../ui";

// The stack's Resources tab (spec 0002 R8.2): what the state manages, unit by
// unit, with the applies that last changed each resource. Attribute values
// show only in labels, read by the server from a fixed list per type.

const pastTense: Record<ChangeAction, string> = { create: "created", update: "updated", replace: "replaced", delete: "deleted" };
const actionTone: Record<ChangeAction, string> = { create: "ok", update: "warn", replace: "rep", delete: "bad" };

// Above this many resources the units start collapsed.
const openLimit = 150;

export function StackResources({ stack }: { stack: Stack }) {
  const { api } = useOrg();
  const res = useData(() => api.resources(stack.id), [api, stack.id], { poll: true, pollMs: 15000 });
  const [q, setQ] = useState("");
  const [unit, setUnit] = useState("");
  const [provider, setProvider] = useState("");
  const [showData, setShowData] = useState(false);
  const [onlyDrift, setOnlyDrift] = useState(false);
  const [toggled, setToggled] = useState<Record<string, boolean>>({});
  const [selected, setSelected] = useState("");

  if (res.error && !res.data) return <ErrorBanner error={res.error} />;
  if (!res.data) return null;
  const { units, drift, state_outside, outside } = res.data;

  if (units.length === 0) {
    if (outside) return <OutsideUnits outside={outside} />;
    return state_outside ? <StateOutside /> : <NoResources />;
  }

  const all = units.flatMap((u) => u.resources);
  const managed = all.filter((r) => r.mode === "managed");
  const byProvider = new Map<string, number>();
  for (const r of managed) byProvider.set(r.provider, (byProvider.get(r.provider) ?? 0) + 1);
  const providers = [...byProvider.entries()].sort((a, b) => b[1] - a[1]);
  const latest = units.reduce<ResourceUnit | null>((a, u) => (!a || (u.updated_at ?? "") > (a.updated_at ?? "") ? u : a), null);

  const needle = q.trim().toLowerCase();
  const filtering = needle !== "" || provider !== "" || onlyDrift;
  const startOpen = all.length <= openLimit;
  const shown = units
    .filter((u) => !unit || u.key === unit)
    .map((u) => ({
      unit: u,
      rows: u.resources.filter(
        (r) =>
          (showData || r.mode === "managed") &&
          (!provider || r.provider === provider) &&
          (!onlyDrift || !!r.drift) &&
          (!needle ||
            r.address.toLowerCase().includes(needle) ||
            r.type.includes(needle) ||
            !!r.label?.name.toLowerCase().includes(needle)),
      ),
    }))
    .filter((g) => g.rows.length > 0 || (!filtering && (g.unit.error || g.unit.resources.length === 0)));
  const single = units.length === 1 && !outside;

  const clear = () => {
    setQ("");
    setUnit("");
    setProvider("");
    setOnlyDrift(false);
  };

  return (
    <>
      <section className="card res-summary">
        <div className="res-stat">
          <strong>{managed.length}</strong>
          <span>managed resources</span>
        </div>
        <div className="res-stat">
          <strong>{all.length - managed.length}</strong>
          <span>data sources</span>
        </div>
        {!single && (
          <div className="res-stat">
            <strong>{units.length}</strong>
            <span>units with state</span>
          </div>
        )}
        {outside && (
          <a className="res-stat" href="#res-outside" onClick={(e) => {
            e.preventDefault();
            document.getElementById("res-outside")?.scrollIntoView({ behavior: "smooth" });
          }}>
            <strong>{outside.units.length}</strong>
            <span className="link-text">
              unit{outside.units.length === 1 ? "" : "s"} with state elsewhere
            </span>
          </a>
        )}
        <div className="res-providers" role="group" aria-label="Filter by provider">
          {providers.map(([p, n]) => (
            <button key={p} className={provider === p ? "chip current" : "chip"} aria-pressed={provider === p} title={p}
              onClick={() => setProvider(provider === p ? "" : p)}>
              <span className="res-dot" style={{ background: providerColor(p) }} />
              <span className="mono">{providerName(p)}</span>
              <span className="muted">{n}</span>
            </button>
          ))}
        </div>
        <p className="res-note">
          From the current state of {single ? "the stack" : "each unit"}.
          {latest?.updated_at && (
            <>
              {" "}Last write <strong>{timeAgo(latest.updated_at)}</strong>
              {latest.run_id ? <> by <RunLink id={latest.run_id} /></> : latest.written_by ? <> by {latest.written_by}</> : null}.
            </>
          )}{" "}
          Only the fields that say what a resource is (its name; for DNS also type and target) are shown. Other attribute
          values never are.
        </p>
      </section>

      {drift && (
        <div className="banner banner-attention">
          <span className="badge warn">Drift</span>
          <span className="banner-text">
            <strong>
              {drift.resources} resource{drift.resources === 1 ? "" : "s"}
            </strong>{" "}
            changed outside First Lume, found by <RunLink id={drift.run_id} label="drift check" /> {timeAgo(drift.at)}.
          </span>
          <button className="small-btn" aria-pressed={onlyDrift} onClick={() => setOnlyDrift(!onlyDrift)}>
            {onlyDrift ? "Show all" : "Show only drifted"}
          </button>
        </div>
      )}

      <div className="res-toolbar">
        <label className="search res-search">
          <span className="sr-only">Search resources</span>
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden>
            <circle cx="11" cy="11" r="7" />
            <path d="M20 20l-3.5-3.5" />
          </svg>
          <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search by name, address or type" />
        </label>
        {!single && (
          <label className="res-select">
            Unit
            <select value={unit} onChange={(e) => setUnit(e.target.value)}>
              <option value="">All units</option>
              {units.map((u) => (
                <option key={u.key} value={u.key}>
                  {u.key || "(root)"}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="res-check">
          <input type="checkbox" checked={showData} onChange={(e) => setShowData(e.target.checked)} /> Show data sources
        </label>
      </div>

      {shown.length === 0 && (
        <Empty>
          No resource matches{needle && <> <strong className="mono">{q}</strong></>}
          {provider && <> with provider {providerName(provider)}</>}.{" "}
          <button className="link" onClick={clear}>
            Clear filters
          </button>
        </Empty>
      )}

      {shown.map(({ unit: u, rows }) => {
        const open = filtering || (toggled[u.key] ?? startOpen) || single;
        return (
          <section key={u.key} className="res-unit">
            {!single && (
              <button className="res-unit-head" aria-expanded={open} onClick={() => setToggled({ ...toggled, [u.key]: !open })}>
                <span className={open ? "res-chev open" : "res-chev"} aria-hidden>
                  ›
                </span>
                <strong className="mono">{u.key || "(root)"}</strong>
                <UnitBadges unit={u} />
                <span className="grow" />
                <span className="muted small">
                  version {u.version} · serial {u.serial} · written {timeAgo(u.updated_at)}
                </span>
              </button>
            )}
            {single && (u.lock || u.error) && (
              <div className="res-unit-head static">
                <UnitBadges unit={u} />
              </div>
            )}
            {u.error && <p className="res-unit-error">{u.error}. The other units are listed normally.</p>}
            {open && rows.length > 0 && (
              <div className="res-table">
                <div className="res-row res-row-head">
                  <span className="res-addr-head">Resource</span>
                  <span>Type</span>
                  <span>Provider</span>
                  <span>Last change</span>
                </div>
                {rows.map((r) => (
                  <Row key={r.address} r={r} unitKey={u.key} selected={selected === `${u.key}:${r.address}`}
                    onPick={() => setSelected(selected === `${u.key}:${r.address}` ? "" : `${u.key}:${r.address}`)} />
                ))}
              </div>
            )}
            {open && rows.length === 0 && !u.error && <p className="res-unit-error muted">This unit’s state has no resources.</p>}
          </section>
        );
      })}
      {outside && <OutsideUnits outside={outside} />}
    </>
  );
}

function UnitBadges({ unit: u }: { unit: ResourceUnit }) {
  const managed = u.resources.filter((r) => r.mode === "managed").length;
  const data = u.resources.length - managed;
  const drifted = u.resources.filter((r) => r.drift).length;
  return (
    <>
      <span className="badge idle">
        {managed} resource{managed === 1 ? "" : "s"}
        {data > 0 && ` · ${data} data`}
      </span>
      {drifted > 0 && <span className="badge warn">{drifted} drifted</span>}
      {u.lock && (
        <span className="badge warn" title={`since ${new Date(u.lock.since).toLocaleString()}`}>
          Locked by {u.lock.run_id ? `run ${u.lock.run_id.slice(0, 8)}` : u.lock.holder}
        </span>
      )}
      {u.error && <span className="badge bad">Can’t read</span>}
    </>
  );
}

function Row({ r, unitKey, selected, onPick }: { r: StateResource; unitKey: string; selected: boolean; onPick: () => void }) {
  const data = r.mode === "data";
  const last = r.history?.[0];
  const [copied, setCopied] = useState<"" | "address" | "name">("");
  return (
    <>
      <button className={selected ? "res-row selected" : "res-row"} aria-expanded={selected} onClick={onPick}>
        <span className="res-addr">
          <ResourceIcon type={r.type} data={data} />
          {r.label ? (
            <span className="res-named">
              <LabelLine label={r.label} data={data} />
              <span className="mono res-addr-tip">{r.address}</span>
            </span>
          ) : (
            <span className={data ? "mono muted" : "mono"}>{r.address}</span>
          )}
        </span>
        <span className="res-type">
          <span className="mono">{r.type}</span>
          <span className="muted small">{resourceKind(r.type)}</span>
        </span>
        <span className="mono small">{providerName(r.provider)}</span>
        <span className="res-last">
          {r.drift ? (
            <span className={r.drift === "create" ? "badge bad" : "badge warn"}>{r.drift === "create" ? "gone" : "drifted"}</span>
          ) : last ? (
            <span className={`badge ${actionTone[last.action]}`}>{pastTense[last.action]}</span>
          ) : data ? (
            <span className="badge idle">read each run</span>
          ) : null}
          {last && !data && (
            <span className="muted small">
              {last.run_id.slice(0, 8)} · {timeAgo(last.at)}
            </span>
          )}
          {!last && !data && !r.drift && <span className="muted small">before First Lume</span>}
        </span>
      </button>
      {selected && (
        <div className="res-detail">
          {r.label?.record && <RecordCol label={r.label} type={r.type} data={data} />}
          <div className="res-detail-col">
            <span className="card-label">Identity</span>
            {!r.label?.record && (
              <span className="res-kind">
                <ResourceIcon type={r.type} data={data} />
                <strong>{r.label?.name ?? resourceKind(r.type)}</strong>
              </span>
            )}
            {r.label && <span className="mono res-dep">{r.address}</span>}
            {r.label && !r.label.record && (
              <span>
                <span className="muted">Kind</span> {resourceKind(r.type)}
              </span>
            )}
            {r.label?.details?.map((d) => (
              <span key={d.key}>
                <span className="muted">{capitalize(d.key)}</span> <span className="mono">{d.value}</span>
              </span>
            ))}
            <span>
              <span className="muted">Mode</span> {data ? "data source" : "managed"}
            </span>
            <span>
              <span className="muted">Module</span> <span className="mono">{r.module || "(root)"}</span>
            </span>
            {unitKey && (
              <span>
                <span className="muted">Unit</span> <span className="mono">{unitKey}</span>
              </span>
            )}
            <span>
              <span className="muted">Provider</span> <span className="mono">{r.provider}</span>
            </span>
            <span>
              <span className="muted">Instances in this block</span> {r.instances}
            </span>
            <span className="res-copy">
              <button className="small-btn"
                onClick={() => {
                  void navigator.clipboard?.writeText(r.address);
                  setCopied("address");
                }}>
                {copied === "address" ? "Copied" : "Copy address"}
              </button>
              {r.label && (
                <button className="small-btn"
                  onClick={() => {
                    void navigator.clipboard?.writeText(r.label!.name);
                    setCopied("name");
                  }}>
                  {copied === "name" ? "Copied" : "Copy name"}
                </button>
              )}
            </span>
          </div>
          <div className="res-detail-col">
            <span className="card-label">Depends on</span>
            {r.depends_on?.length ? (
              r.depends_on.map((d) => (
                <span key={d} className="mono res-dep">
                  {d}
                </span>
              ))
            ) : (
              <span className="muted">Nothing in this {unitKey ? "unit" : "stack"}</span>
            )}
          </div>
          <div className="res-detail-col">
            <span className="card-label">History</span>
            {r.drift && (
              <span className="res-event">
                <span className={r.drift === "create" ? "badge bad" : "badge warn"}>{r.drift === "create" ? "gone" : "drifted"}</span>
                <span className="muted">found by the latest drift check</span>
              </span>
            )}
            {r.history?.length ? (
              r.history.map((h) => (
                <span key={h.run_id} className="res-event">
                  <span className={`badge ${actionTone[h.action]}`}>{pastTense[h.action]}</span>
                  <RunLink id={h.run_id} label={h.run_type} />
                  <span className="muted">
                    {timeAgo(h.at)} · {h.by}
                  </span>
                </span>
              ))
            ) : (
              <span className="muted">{data ? "Data sources are read on every run." : "No apply in First Lume changed it yet."}</span>
            )}
          </div>
        </div>
      )}
    </>
  );
}

function RecordCol({ label, type, data }: { label: ResourceLabel; type: string; data: boolean }) {
  const rec = label.record!;
  return (
    <div className="res-detail-col">
      <span className="card-label">Record</span>
      <span className="res-kind">
        <ResourceIcon type={type} data={data} />
        <strong className="res-record-name">{label.name}</strong>
      </span>
      {rec.type && (
        <span>
          <span className="muted">Type</span> <span className="res-rtype">{rec.type}</span>
        </span>
      )}
      {rec.target && (
        <span>
          <span className="muted">Points to</span> <span className="mono">{rec.target}</span>
        </span>
      )}
      {rec.proxied !== undefined && (
        <span>
          <span className="muted">Proxy</span>{" "}
          {rec.proxied ? (
            <>
              <span className="res-proxied">proxied</span> <span className="muted">through Cloudflare</span>
            </>
          ) : (
            "DNS only"
          )}
        </span>
      )}
      {!!rec.ttl && (
        <span>
          <span className="muted">TTL</span> {rec.ttl === 1 ? "auto" : `${rec.ttl} s`}
        </span>
      )}
      {rec.zone && (
        <span>
          <span className="muted">Zone</span> <span className="mono">{rec.zone}</span>
        </span>
      )}
    </div>
  );
}

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

function RunLink({ id, label }: { id: string; label?: string }) {
  const { link } = useOrg();
  return (
    <Link to={link(`r/${id}`)}>
      run {id.slice(0, 8)}
      {label && ` · ${label}`}
    </Link>
  );
}

function NoResources() {
  return (
    <section className="card res-empty">
      <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" aria-hidden>
        <path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z" />
        <path d="M12 12l8-4.5M12 12v9M12 12L4 7.5" />
      </svg>
      <h2>No resources yet</h2>
      <p>Resources show up here after the first apply writes the state. A plan alone doesn’t create them.</p>
    </section>
  );
}

// OutsideUnits names the units whose state the latest run found in another
// backend (spec 0002 R2.3): Firstlume can't list what they manage.
function OutsideUnits({ outside }: { outside: NonNullable<Resources["outside"]> }) {
  const single = outside.units.length === 1 && outside.units[0].unit === "";
  const byBackend = new Map<string, number>();
  for (const u of outside.units) byBackend.set(u.backend, (byBackend.get(u.backend) ?? 0) + 1);
  const found = [...byBackend.entries()].sort((a, b) => b[1] - a[1]);
  const run = <RunLink id={outside.run_id} label={outside.run_type} />;
  return (
    <section id="res-outside" className="card res-outside res-outside-units">
      <div className="res-outside-head">
        <h2>First Lume can’t list these resources</h2>
        <p>
          {single ? <>The last run ({run}) found</> : <>{run} found</>}{" "}
          {found.map(([b, n], i) => (
            <span key={b}>
              {i > 0 && (i === found.length - 1 ? " and " : ", ")}
              <BackendName backend={b} />
              {!single && ` in ${n} unit${n === 1 ? "" : "s"}`}
            </span>
          ))}
          , so {single ? "the" : "their"} state lives there and First Lume never reads it. Switch the code to{" "}
          <span className="mono">backend "http" {"{}"}</span> and import the current state to see{" "}
          {single ? "the resources" : "them"} here.
        </p>
      </div>
      {!single && (
        <div className="res-table">
          <div className="res-outside-row res-row-head">
            <span>Unit</span>
            <span>Backend</span>
            <span>Seen in</span>
          </div>
          {outside.units.map((u) => (
            <div key={u.unit} className="res-outside-row">
              <strong className="mono">{u.unit || "(root)"}</strong>
              <span>
                <span className={u.backend === "local" ? "badge bad" : "badge idle"}>{u.backend}</span>
              </span>
              <span className="muted small">
                {run} · {timeAgo(outside.at)}
              </span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function BackendName({ backend }: { backend: string }) {
  if (backend === "local") return <>no remote backend (local state, gone when the run ends)</>;
  return (
    <>
      <span className="mono">backend "{backend}"</span>
      {backend === "http" && " on another server"}
    </>
  );
}

function StateOutside() {
  return (
    <section className="card res-outside">
      <h2>First Lume can’t list these resources</h2>
      <p>
        An apply ran, but no state was written to First Lume: the code keeps it in another backend. Switch it to{" "}
        <span className="mono">backend "http" {"{}"}</span> and import the current state to see the resources here.
      </p>
    </section>
  );
}
