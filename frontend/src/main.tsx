import React, { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

type Actor = { email: string; tenant_id: string; tenant_name: string; timezone: string };
type Certificate = { id: string; serial_number: string; learner_name: string; course_title: string; issued_at: string; issued_local: string; issue_date: string; status: string };
type Course = { id: string; title: string };
type List = { items: Certificate[]; actor: Actor; total: number; page: number; page_size: number };
type IssueResult = { operation_id: string; status: string; summary: Record<string, number>; outcomes: { learner_id: string; outcome: string; reason?: string; certificate_id?: string }[] };
class APIError extends Error { constructor(message: string, public status: number) { super(message); } }

async function request<T>(path: string, actor: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, { ...options, headers: { 'X-Actor-Email': actor, ...options.headers } });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new APIError(body.error || `Request failed (${response.status})`, response.status);
  }
  return response.json();
}
const attemptStorageKey = (actor: string, course: string) => `oceanai-issuance:${actor}:${course}`;

function App() {
  const [email, setEmail] = useState('alice@east.test');
  const [actors, setActors] = useState<Actor[]>([]);
  const [courses, setCourses] = useState<Course[]>([]);
  const [course, setCourse] = useState('');
  const [data, setData] = useState<List | null>(null);
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [sort, setSort] = useState('issue_date');
  const [direction, setDirection] = useState('desc');
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [connected, setConnected] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [issuing, setIssuing] = useState(false);
  const [retryIssue, setRetryIssue] = useState(false);
  const [issueResult, setIssueResult] = useState<IssueResult | null>(null);
  const [actionError, setActionError] = useState('');
  const [detail, setDetail] = useState<Certificate | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [exporting, setExporting] = useState(false);
  const detailRequest = useRef<AbortController | null>(null);
  const issueLock = useRef(false);
  const revokeLock = useRef(false);

  const query = new URLSearchParams({ search, sort, direction });
  const listQuery = `${query}&page=${page}&page_size=25`;
  const actor = actors.find((item) => item.email === email);
  const busy = issuing || revoking;

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError(''); setData(null);
    Promise.all([
      request<{ database: string }>('/api/health', email, { signal: controller.signal }),
      request<{ items: Actor[] }>('/api/actors', email, { signal: controller.signal }),
      request<{ items: Course[] }>('/api/courses', email, { signal: controller.signal }),
      request<List>(`/api/certificates?${listQuery}`, email, { signal: controller.signal }),
    ]).then(([health, nextActors, nextCourses, nextData]) => {
      if (controller.signal.aborted) return;
      setConnected(health.database === 'connected'); setActors(nextActors.items); setCourses(nextCourses.items); setData(nextData);
      setCourse((current) => nextCourses.items.some((item) => item.id === current) ? current : (nextCourses.items.find((item) => item.title === 'Safety 101')?.id ?? nextCourses.items[0]?.id ?? ''));
    }).catch((err: Error) => {
      if (!controller.signal.aborted) { setError(err.message); setConnected(false); }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [email, listQuery, refresh]);

  useEffect(() => {
    setRetryIssue(Boolean(course && sessionStorage.getItem(attemptStorageKey(email, course))));
    setIssueResult(null); setActionError('');
  }, [email, course]);
  useEffect(() => () => detailRequest.current?.abort(), []);

  function closeDetail() {
    detailRequest.current?.abort(); setDetail(null); setDetailLoading(false); setDetailError(''); setConfirmRevoke(false);
  }
  function switchActor(next: string) {
    closeDetail(); setEmail(next); setData(null); setCourses([]); setCourse(''); setPage(1); setIssueResult(null); setActionError('');
  }
  async function issue() {
    if (issueLock.current || !course) return;
    issueLock.current = true; setIssuing(true); setActionError(''); setIssueResult(null);
    const storageKey = attemptStorageKey(email, course);
    try {
      const key = sessionStorage.getItem(storageKey) || crypto.randomUUID();
      sessionStorage.setItem(storageKey, key);
      const result = await request<IssueResult>(`/api/courses/${encodeURIComponent(course)}/issue`, email, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key }, body: '{}', signal: AbortSignal.timeout(35000),
      });
      sessionStorage.removeItem(storageKey); setRetryIssue(false); setIssueResult(result); setRefresh((value) => value + 1);
    } catch (err) {
      const definite = err instanceof APIError && err.status >= 400 && err.status < 500;
      if (definite) sessionStorage.removeItem(storageKey);
      setRetryIssue(!definite);
      setActionError(`${(err as Error).message}${definite ? '' : '. Retry issuance to recover the original operation safely.'}`);
    } finally { issueLock.current = false; setIssuing(false); }
  }
  async function openDetail(id: string) {
    detailRequest.current?.abort();
    const controller = new AbortController(); detailRequest.current = controller;
    setDetail(null); setDetailError(''); setDetailLoading(true); setConfirmRevoke(false);
    try {
      const item = await request<Certificate>(`/api/certificates/${encodeURIComponent(id)}`, email, { signal: controller.signal });
      if (!controller.signal.aborted) setDetail(item);
    } catch (err) { if (!controller.signal.aborted) setDetailError((err as Error).message); }
    finally { if (!controller.signal.aborted) setDetailLoading(false); }
  }
  async function revoke() {
    if (!detail || revokeLock.current) return;
    revokeLock.current = true; setRevoking(true); setDetailError('');
    try {
      setDetail(await request<Certificate>(`/api/certificates/${encodeURIComponent(detail.id)}/revoke`, email, { method: 'POST', signal: AbortSignal.timeout(20000) }));
      setConfirmRevoke(false); setRefresh((value) => value + 1);
    } catch (err) { setDetailError(`${(err as Error).message}. Retrying revocation is safe.`); }
    finally { revokeLock.current = false; setRevoking(false); }
  }
  async function exportCSV() {
    setExporting(true); setActionError('');
    try {
      const response = await fetch(`/api/certificates/export?${query}`, { headers: { 'X-Actor-Email': email }, signal: AbortSignal.timeout(65000) });
      if (!response.ok) { const body = await response.json(); throw new Error(body.error || 'Export failed'); }
      const url = URL.createObjectURL(await response.blob());
      const link = document.createElement('a'); link.href = url; link.download = `${actor?.tenant_id ?? 'tenant'}-certificates.csv`;
      document.body.appendChild(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (err) { setActionError(`CSV export failed: ${(err as Error).message}`); }
    finally { setExporting(false); }
  }

  return <div className="shell">
    <header className="topbar"><a className="brand" href="/"><span className="brandmark">O</span> OceanAI <span className="brand-sub">/ Admin</span></a><span className="pill">Certificate operations</span></header>
    <main>
      <div className="intro"><p className="eyebrow">TRAINING OPERATIONS</p><h1>Certificate console</h1><p className="lede">Review achievements, issue certificates, and manage their lifecycle.</p></div>
      <section className="connection" aria-label="Application connection status"><div className="connection-icon" aria-hidden="true">↗</div><div><h2>{connected ? 'Application connected' : loading ? 'Checking connection…' : 'Connection needs attention'}</h2><p>All records belong to the selected administrator’s tenant.</p></div><span className={`connection-state ${connected ? 'connected' : ''}`} role="status">{connected ? 'Live database' : 'Checking'}</span></section>
      <section className="workspace" aria-labelledby="certificates-title">
        <div className="toolbar"><div><p className="eyebrow">YOUR WORKSPACE</p><h2 id="certificates-title">Certificates</h2></div><div className="controls"><label htmlFor="actor">Demo actor<select id="actor" value={email} onChange={(event) => switchActor(event.target.value)} disabled={busy || actors.length === 0}>{actors.length === 0 ? <option value={email}>{email}</option> : actors.map((item) => <option key={item.email} value={item.email}>{item.email} · {item.tenant_name}</option>)}</select></label><button disabled={loading || busy} onClick={() => setRefresh((value) => value + 1)}>Refresh</button></div></div>
        <div className="tenant-strip"><span><span className="dot" />{actor?.tenant_name ?? 'East'} tenant</span><span>{actor?.timezone ?? 'Australia/Sydney'}</span></div>
        <form className="filters" onSubmit={(event) => { event.preventDefault(); setSearch(searchInput.trim()); setPage(1); }}>
          <label htmlFor="search">Learner or serial number<input id="search" value={searchInput} maxLength={200} onChange={(event) => setSearchInput(event.target.value)} placeholder="Search certificates" /></label><button type="submit">Search</button>
          <label htmlFor="sort">Sort by<select id="sort" value={sort} onChange={(event) => { setSort(event.target.value); setPage(1); }}><option value="issue_date">Issue date</option><option value="learner_name">Learner name</option><option value="serial_number">Serial number</option></select></label>
          <label htmlFor="direction">Order<select id="direction" value={direction} onChange={(event) => { setDirection(event.target.value); setPage(1); }}><option value="desc">Descending</option><option value="asc">Ascending</option></select></label>
          <button type="button" disabled={exporting || busy || !actor} onClick={exportCSV}>{exporting ? 'Exporting…' : 'Export CSV'}</button>
        </form>
        {error ? <div className="message error" role="alert"><strong>Couldn’t load certificates.</strong><p>{error}. Use Refresh to try again.</p></div> : loading ? <div className="message" role="status">Loading certificates…</div> : data?.items.length === 0 ? <div className="message">No matching certificates.</div> : <div className="table-wrap"><table><thead><tr><th>Learner</th><th>Course</th><th>Serial number</th><th>Issued · tenant time</th><th>Status</th><th>Details</th></tr></thead><tbody>{data?.items.map((cert) => <tr key={cert.id}><td><strong>{cert.learner_name}</strong></td><td>{cert.course_title}</td><td className="serial">{cert.serial_number}</td><td><time dateTime={cert.issued_at}>{cert.issued_local}</time></td><td><span className={`status ${cert.status === 'REVOKED' ? 'revoked' : ''}`}>{cert.status}</span></td><td><button disabled={busy} onClick={() => openDetail(cert.id)} aria-label={`View ${cert.serial_number}`}>View</button></td></tr>)}</tbody></table></div>}
        <div className="table-footer"><span>{data ? `${data.total} matching certificate${data.total === 1 ? '' : 's'}` : 'Waiting for data'}</span><div className="pagination"><button disabled={loading || page === 1} onClick={() => setPage(page - 1)}>Previous</button><span>Page {page}{data ? ` of ${Math.max(1, Math.ceil(data.total / data.page_size))}` : ''}</span><button disabled={loading || !data || page * data.page_size >= data.total} onClick={() => setPage(page + 1)}>Next</button></div></div>
      </section>
      <section className="workspace issue-panel" aria-labelledby="issue-title"><div className="toolbar"><div><p className="eyebrow">COURSE COMPLETIONS</p><h2 id="issue-title">Issue certificates</h2><p className="hint">Issue to completed learners. Existing certificates and incomplete enrollments are skipped.</p></div><div className="controls"><label htmlFor="course">Course<select id="course" value={course} onChange={(event) => setCourse(event.target.value)} disabled={busy || courses.length === 0}>{courses.map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}</select></label><button className="primary" disabled={busy || !course} onClick={issue}>{issuing ? 'Issuing…' : retryIssue ? 'Retry issuance' : 'Issue certificates'}</button></div></div>
        {issuing && <p className="inline-message" role="status">Issuing certificates. Please keep this page open.</p>}
        {actionError && <p className="inline-message error" role="alert">{actionError}</p>}
        {issueResult && <div className="results" role="status"><strong>Completed: {issueResult.summary.issued} issued · {issueResult.summary.skipped} skipped · {issueResult.summary.failed} failed</strong><p className="hint">Operation {issueResult.operation_id}</p><ul>{issueResult.outcomes.map((item) => <li key={item.learner_id}>{item.learner_id}: <strong>{item.outcome}</strong>{item.reason ? ` (${item.reason.replaceAll('_', ' ')})` : ''}</li>)}</ul>{issueResult.outcomes.length === 0 && <p>No enrollments in this course.</p>}</div>}
      </section>
      {(detail || detailLoading || detailError) && <section className="workspace detail-panel" aria-labelledby="detail-title"><div className="toolbar"><h2 id="detail-title">Certificate details</h2><button disabled={revoking} onClick={closeDetail}>Close details</button></div>{detailLoading && <p className="inline-message" role="status">Loading certificate…</p>}{detailError && <p className="inline-message error" role="alert">{detailError}</p>}{detail && <div className="detail-content"><dl><dt>Serial number</dt><dd>{detail.serial_number}</dd><dt>Learner</dt><dd>{detail.learner_name}</dd><dt>Course</dt><dd>{detail.course_title}</dd><dt>Issued ({actor?.timezone})</dt><dd>{detail.issued_local}</dd><dt>Status</dt><dd>{detail.status}</dd></dl>{detail.status === 'ACTIVE' && (confirmRevoke ? <div className="confirmation"><p>Revoke this certificate? This action is permanent.</p><button className="danger" disabled={revoking} onClick={revoke}>{revoking ? 'Revoking…' : 'Confirm revocation'}</button><button disabled={revoking} onClick={() => setConfirmRevoke(false)}>Cancel</button></div> : <button className="danger" disabled={busy} onClick={() => setConfirmRevoke(true)}>Revoke certificate</button>)}</div>}</section>}
      <footer>Dates use the selected tenant’s timezone. CSV exports include every matching row in the selected order.</footer>
    </main>
  </div>;
}

createRoot(document.getElementById('root')!).render(<React.StrictMode><App /></React.StrictMode>);
