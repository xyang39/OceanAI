#!/usr/bin/env python3
"""Export recorded public interactions and tool activity; never export model internals."""
import argparse
import collections
import csv
import hashlib
import json
from pathlib import Path
import re
from datetime import datetime, timezone

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('source', type=Path, help='Native Codex rollout JSONL for this session')
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
out = root / 'ai' / 'sessions'
out.mkdir(parents=True, exist_ok=True)
source_bytes = args.source.read_bytes()
lines = source_bytes.splitlines(keepends=True)
# A live log can end with a partially written record. Include only complete JSON records.
records = []
for n, line in enumerate(lines, 1):
    try:
        records.append((n, json.loads(line)))
    except json.JSONDecodeError:
        if n != len(lines):
            raise
        break
meta = records[0][1]['payload']
sid = meta['id']
if sid != '01a2010b-42bb-7be2-8a7c-0c0593e8127e':
    raise SystemExit('This project exporter is scoped to the assessment session only.')
base = f'2026-11-22-{sid}'
redactions = []
models = sorted({r['payload']['model'] for _, r in records if r['type'] == 'turn_context' and r['payload'].get('model')})
# This retrieval contains unrelated chat titles; the other retrieves duplicate conversation
# history with internal runtime metadata. Original public records are exported independently.
private_retrieval_calls = {
    'call_5833c1e4eb334edab0e13597024985ce': 'unrelated private chat inventory; target session exported independently',
    'call_9f9a220d1b2f46a6b3c25754477a689e': 'duplicate history retrieval containing internal metadata; public originals exported independently',
}

def clean(value, line, key=''):
    if isinstance(value, dict):
        if value.get('type') in ('image', 'input_image', 'image_url'):
            redactions.append({'source_line': line, 'reason': 'binary image omitted; see saved screenshots and image paths'})
            return {'type': value['type'], 'omitted': '[REDACTED: binary image; see saved screenshots and image paths]'}
        return {k: clean(v, line, k) for k, v in value.items()
                if k not in ('internal_chat_message_metadata_passthrough', 'encrypted_content', 'raw_content', 'raw_content_text', 'environmentId')}
    if isinstance(value, list):
        return [clean(v, line, key) for v in value]
    if isinstance(value, str):
        value, count = re.subn(r'oceanai_local_demo', '[REDACTED: local demo database password]', value)
        if count:
            redactions.append({'source_line': line, 'reason': 'local demo database password', 'occurrences': count})
        value, count = re.subn(r'data:image/[^;\s]+;base64,[A-Za-z0-9+/=]+', '[REDACTED: inline binary image]', value)
        if count:
            redactions.append({'source_line': line, 'reason': 'inline binary image', 'occurrences': count})
        return value
    return value

interactions = []
activity = []
excluded = collections.Counter()
call_names = set()
activity_types = {'CommandExecution', 'FileChange', 'McpToolCall', 'ImageView', 'Extension', 'Plan'}
for n, rec in records:
    payload = rec.get('payload', {})
    typ = payload.get('type')
    if rec['type'] == 'response_item':
        if typ == 'message' and payload.get('role') in ('user', 'assistant'):
            if payload.get('channel') in ('analysis', 'summary'):
                excluded['internal message'] += 1
                continue
            item = {k: payload[k] for k in ('type', 'id', 'role', 'phase', 'content') if k in payload}
        elif typ in ('function_call', 'custom_tool_call', 'function_call_output', 'custom_tool_call_output'):
            item = {k: payload[k] for k in ('type', 'id', 'call_id', 'name', 'namespace', 'arguments', 'input', 'output', 'status') if k in payload}
            if payload.get('name'):
                call_names.add((payload.get('namespace', '') + '.' + payload['name']).strip('.'))
            if 'output' in item and payload.get('call_id') in private_retrieval_calls:
                reason = private_retrieval_calls[payload['call_id']]
                item['output'] = '[REDACTED: ' + reason + ']'
                redactions.append({'source_line': n, 'reason': reason})
        else:
            excluded['response_item/' + str(typ)] += 1
            continue
        interactions.append({'source_line': n, 'timestamp': rec['timestamp'], 'payload': clean(item, n)})
    elif rec['type'] == 'event_msg' and typ == 'item_completed':
        item = payload.get('item', {})
        if item.get('type') not in activity_types:
            excluded['event/' + str(item.get('type'))] += 1
            continue
        item = dict(item)
        if item.get('type') == 'McpToolCall':
            call_names.add(item.get('server', '') + '.' + item.get('tool', ''))
            if item.get('server') == 'codex_app' and item.get('tool') in ('list_threads', 'read_thread'):
                reason = 'history retrieval metadata; target session public originals exported independently'
                item['result'] = '[REDACTED: ' + reason + ']'
                redactions.append({'source_line': n, 'reason': reason})
        activity.append({'source_line': n, 'timestamp': rec['timestamp'], 'turn_id': payload.get('turn_id'),
                         'started_at_ms': payload.get('started_at_ms'), 'completed_at_ms': payload.get('completed_at_ms'),
                         'item': clean(item, n)})
    else:
        excluded[rec['type']] += 1

session_file = out / (base + '.jsonl')
activity_file = out / (base + '-tool-activity.jsonl')
transcript_file = out / (base + '.md')
for path, rows in ((session_file, interactions), (activity_file, activity)):
    path.write_text(''.join(json.dumps(r, ensure_ascii=False) + '\n' for r in rows))

md = [f'# Session {sid}', '', 'Tool: Codex Desktop. Model recorded: ' + ', '.join(models) + '.', '',
      'Public messages are reproduced in chronological order. Tool calls, outputs, commands, errors, and file diffs are in the adjacent JSONL exports. This is a redacted extraction, not a fabricated native export. Source line numbers refer to the original rollout snapshot.', '',
      'Timestamps use UTC. For scope, redactions, and limitations see [completeness declaration](../completeness-declaration.md).', '']
for rec in interactions:
    p = rec['payload']
    if p['type'] != 'message':
        continue
    n = rec['source_line']
    md += [f'<a id="source-L{n}"></a>', f'## L{n} — {rec["timestamp"]} — {p["role"]}' + (f' ({p["phase"]})' if p.get('phase') else ''), '']
    for content in p.get('content', []):
        if isinstance(content, dict):
            md += [content.get('text', json.dumps(content, ensure_ascii=False)), '']
        else:
            md += [str(content), '']
transcript_file.write_text('\n'.join(md))

last_n, last = records[-1]
manifest = {
    'session_id': sid, 'title': 'Review interview assessment requirements',
    'tool': 'Codex Desktop', 'cli_version': meta.get('cli_version'), 'models': models,
    'timezone': 'UTC', 'started_at': records[0][1]['timestamp'], 'snapshot_ended_at': last['timestamp'],
    'exported_at': datetime.now(timezone.utc).isoformat(), 'session_still_active': True,
    'source_filename': args.source.name, 'source_records': last_n,
    'source_prefix_sha256': hashlib.sha256(b''.join(lines[:last_n])).hexdigest(),
    'interaction_records': len(interactions), 'tool_activity_records': len(activity),
    'message_counts': dict(collections.Counter(r['payload']['role'] for r in interactions if r['payload']['type'] == 'message')),
    'tools_recorded': sorted(call_names), 'excluded_record_categories': dict(excluded),
    'redactions': redactions,
    'files': {p.name: {'bytes': p.stat().st_size, 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()}
              for p in (session_file, activity_file, transcript_file)},
}
(out / (base + '-manifest.json')).write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')
with (root / 'ai/session-index.csv').open('w', newline='') as f:
    writer = csv.writer(f)
    writer.writerow(['session_id', 'tool', 'model', 'started_at', 'ended_at', 'export_file', 'purpose', 'related_commits'])
    writer.writerow([sid, 'Codex Desktop', ';'.join(models), manifest['started_at'], manifest['snapshot_ended_at'],
                     'sessions/' + session_file.name,
                     'Assessment reading; framework; design; A1-A7 implementation; verification; README review; evidence packaging snapshot',
                     'UNAVAILABLE: workspace has no Git history at export'])
print(json.dumps({k: manifest[k] for k in ('session_id', 'snapshot_ended_at', 'source_records', 'interaction_records', 'tool_activity_records', 'message_counts')}, indent=2))
