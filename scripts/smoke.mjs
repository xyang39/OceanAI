import assert from 'node:assert/strict';

const base = process.env.BASE_URL || 'http://localhost:8080';
let checks = 0;

async function check(name, fn) {
  await fn();
  checks += 1;
  console.log(`PASS ${name}`);
}

async function get(path, email, status = 200) {
  const response = await fetch(`${base}${path}`, {
    headers: email ? { 'X-Actor-Email': email } : {},
    signal: AbortSignal.timeout(10000),
  });
  assert.equal(response.status, status, `${path}: expected HTTP ${status}, got ${response.status}`);
  return response.json();
}

await check('compiled React page and JavaScript are served', async () => {
  const response = await fetch(base, { signal: AbortSignal.timeout(10000) });
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /Certificate Console/);
  const asset = html.match(/src="(\/assets\/[^\"]+\.js)"/);
  assert.ok(asset, 'production JS bundle is referenced');
  const bundle = await fetch(`${base}${asset[1]}`);
  assert.equal(bundle.status, 200);
  assert.match(bundle.headers.get('content-type'), /javascript/);
});

await check('Nginx → Gin → PostgreSQL readiness', async () => {
  assert.deepEqual(await get('/api/health'), { status: 'ok', database: 'connected', service: 'oceanai-gin' });
});

await check('both seeded demo actors are discoverable', async () => {
  const body = await get('/api/actors');
  assert.deepEqual(body.items.map((actor) => actor.email), ['alice@east.test', 'bob@west.test']);
});

for (const [tenant, email, local, date] of [
  ['east', 'alice@east.test', '2026-01-11 00:30', '2026-01-11'],
  ['west', 'bob@west.test', '2026-01-10 21:30', '2026-01-10'],
]) {
  await check(`${tenant}: persisted certificate, actor scope, and local date`, async () => {
    const list = await get('/api/certificates', email);
    assert.equal(list.actor.tenant_id, tenant);
    assert.ok(list.total >= 1);
    const certificate = await get(`/api/certificates/${tenant}-demo-certificate`, email);
    assert.equal(certificate.id, `${tenant}-demo-certificate`);
    assert.equal(certificate.learner_name, 'Alex Lee');
    assert.equal(certificate.course_title, 'Safety 101');
    assert.equal(certificate.issued_local, local);
    assert.equal(certificate.issue_date, date);
    assert.ok(['ACTIVE', 'REVOKED'].includes(certificate.status));
    assert.deepEqual(await get(`/api/certificates/${certificate.id}`, email), certificate);
    console.log(`     ${email} → ${certificate.serial_number} → ${certificate.issued_local}`);
  });
}

await check('cross-tenant detail is indistinguishable from a missing certificate', async () => {
  const denied = await get('/api/certificates/east-demo-certificate', 'bob@west.test', 404);
  const missing = await get('/api/certificates/not-real', 'bob@west.test', 404);
  assert.deepEqual(denied, missing);
  await get('/api/certificates/west-demo-certificate', 'alice@east.test', 404);
});

await check('missing and unknown actors are rejected', async () => {
  await get('/api/certificates', undefined, 401);
  await get('/api/certificates', 'unknown@example.test', 401);
});

console.log(`\n${checks} public-service smoke checks passed. Run sh scripts/verify.sh for isolated A1–A7 checks.`);
