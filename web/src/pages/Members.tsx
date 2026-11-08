import { Fragment, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import type { Member } from "../api";
import { useOrg } from "../org";
import { useSession } from "../session";
import { Empty, ErrorBanner, Initials, useAction, useData } from "../ui";

const dayMs = 24 * 60 * 60 * 1000;

function sinceLabel(iso: string): string {
  const d = new Date(iso);
  if (d.toDateString() === new Date().toDateString()) return "Today";
  return d.toLocaleDateString(undefined, { dateStyle: "medium" });
}

function expiresLabel(iso: string): string {
  const days = Math.ceil((new Date(iso).getTime() - Date.now()) / dayMs);
  return days <= 1 ? "in 1 day" : `in ${days} days`;
}

function MemberAvatar({ member }: { member: Member }) {
  const [broken, setBroken] = useState(false);
  if (member.avatar_url && !broken) {
    return <img className="avatar" src={member.avatar_url} alt="" width={30} height={30} onError={() => setBroken(true)} />;
  }
  return <Initials name={member.name || member.login} size={30} />;
}

/** Who is in the organization, who is invited, and inviting and removing (R7.18). */
export function Members() {
  const { org, api } = useOrg();
  const { me, reload, providerName } = useSession();
  const navigate = useNavigate();
  const list = useData(() => api.members(), [api]);
  const [login, setLogin] = useState("");
  const [notice, setNotice] = useState<{ tone: "success" | "attention"; title: string; text: string } | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const action = useAction();

  async function invite(e: FormEvent) {
    e.preventDefault();
    setNotice(null);
    await action.run(async () => {
      const r = await api.invite(login.trim());
      setLogin("");
      if (r.member) {
        setNotice({
          tone: "success",
          title: `${r.member.name || r.member.login} now has access.`,
          text: `They already use First Lume, so ${org.name} appears right away.`,
        });
      } else if (r.invite) {
        setNotice({
          tone: "attention",
          title: `${r.invite.login} hasn’t signed in to First Lume yet.`,
          text: "The invitation is pending and takes effect on their first sign-in.",
        });
      }
      await list.reload();
    });
  }

  async function remove(m: Member) {
    await action.run(async () => {
      await api.removeMember(m.user_id);
      setRemoving(null);
      setNotice(null);
      if (m.user_id === me.user?.id) {
        // Leaving may end access to this page: start over from the list.
        await reload();
        navigate("/");
        return;
      }
      await list.reload();
    });
  }

  const cancel = (id: string) =>
    action.run(async () => {
      await api.cancelInvite(id);
      await list.reload();
    });

  return (
    <>
      <section id="members" className="card">
        <h2 className="section-title">Invite someone</h2>
        <p className="muted">Only people invited here can see {org.name}. Everyone in the organization is an administrator of it for now.</p>
        <form className="inline-form members-form" onSubmit={invite}>
          <label className="field members-login">
            {providerName} username
            <input value={login} onChange={(e) => setLogin(e.target.value)} placeholder="e.g. dani.r" autoComplete="off" />
          </label>
          <button className="primary" disabled={!login.trim() || action.busy}>
            Invite
          </button>
        </form>
        {notice && (
          <div role="status" className={`login-msg login-msg-${notice.tone}`}>
            <strong>{notice.title}</strong> {notice.text}
          </div>
        )}
        <ErrorBanner error={list.error ?? action.error} onDismiss={action.error ? () => action.setError(null) : undefined} />
      </section>

      <section className="card">
        <h2 className="section-title">
          Members {list.data && <span className="muted members-count">{list.data.members.length}</span>}
        </h2>
        {list.data &&
          (list.data.members.length === 0 ? (
            <Empty>No members yet. Installation administrators can still open this organization.</Empty>
          ) : (
            <div className="table-scroll">
              <table className="table plain-table">
                <thead>
                  <tr>
                    <th>Person</th>
                    <th>Member since</th>
                    <th>
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {list.data.members.map((m) => {
                    const you = m.user_id === me.user?.id;
                    return (
                      <Fragment key={m.user_id}>
                        <tr>
                          <td>
                            <span className="member-person">
                              <MemberAvatar member={m} />
                              <span className="member-id">
                                <strong>
                                  {m.name || m.login} {you && <span className="badge idle">You</span>}
                                </strong>
                                <span className="mono muted">{m.login}</span>
                              </span>
                            </span>
                          </td>
                          <td className="muted">{sinceLabel(m.since)}</td>
                          <td className="right">
                            {removing !== m.user_id && <button onClick={() => setRemoving(m.user_id)}>{you ? "Leave" : "Remove"}</button>}
                          </td>
                        </tr>
                        {removing === m.user_id && (
                          <tr>
                            <td colSpan={3}>
                              <div role="alertdialog" aria-label={you ? `Leave ${org.name}` : `Remove ${m.login}`} className="member-confirm">
                                <span>
                                  <strong>{you ? `Leave ${org.name}?` : `Remove ${m.name || m.login} from ${org.name}?`}</strong>{" "}
                                  {you ? "You lose access right away." : "They lose access right away, even with a session open."} Runs already
                                  started keep going.
                                </span>
                                <button onClick={() => setRemoving(null)}>Keep</button>
                                <button className="danger" disabled={action.busy} onClick={() => void remove(m)}>
                                  {you ? "Leave" : "Remove"}
                                </button>
                              </div>
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ))}
      </section>

      {list.data && list.data.invites.length > 0 && (
        <section className="card">
          <h2 className="section-title">
            Pending invitations <span className="muted members-count">{list.data.invites.length}</span>
          </h2>
          <p className="muted">First Lume can’t tell whether a username exists in {providerName}. If someone still has no access after signing in, check the spelling.</p>
          <div className="table-scroll">
            <table className="table plain-table">
              <thead>
                <tr>
                  <th>Username</th>
                  <th>Invited by</th>
                  <th>Expires</th>
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {list.data.invites.map((i) => (
                  <tr key={i.id}>
                    <td className="mono">{i.login}</td>
                    <td className="muted">{i.invited_by}</td>
                    <td className="muted">{expiresLabel(i.expires_at)}</td>
                    <td className="right">
                      <button disabled={action.busy} onClick={() => void cancel(i.id)}>
                        Cancel invitation
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}

      <p className="hint">People in this installation’s admin groups can also see {org.name}. They aren’t listed as members.</p>
    </>
  );
}
