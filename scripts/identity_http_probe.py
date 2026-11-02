#!/usr/bin/env python3
"""Test-only HTTPS browser-cookie client; secrets come from stdin, never argv.
The two-namespace suite runs this helper in each real network namespace.
Cookie jars exist only in the private ephemeral test directory.
"""
import http.cookiejar
import json
import os
import ssl
import sys
import urllib.error
import urllib.request
from pathlib import Path


def request(q):
    jar = http.cookiejar.LWPCookieJar(q['jar']) if q.get('jar') else http.cookiejar.CookieJar()
    if q.get('jar') and Path(q['jar']).exists():
        jar.load(ignore_discard=True, ignore_expires=False)
    ctx = ssl.create_default_context(cafile=q['ca'])
    opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx), urllib.request.HTTPCookieProcessor(jar))
    headers = dict(q.get('headers') or {})
    payload = None
    if 'body' in q:
        headers['Content-Type'] = 'application/json'
        payload = json.dumps(q['body'], ensure_ascii=False).encode('utf-8')
    req = urllib.request.Request(q['url'], data=payload, method=q.get('method','GET'), headers=headers)
    try:
        response = opener.open(req, timeout=8)
    except urllib.error.HTTPError as exc:
        response = exc
    with response:
        content = response.read()
        out = {'status':response.status, 'data':json.loads(content) if content else None}
    if q.get('jar'):
        jar.save(ignore_discard=True, ignore_expires=False)
        os.chmod(q['jar'], 0o600)
    return out

if __name__ == '__main__':
    print(json.dumps(request(json.load(sys.stdin)), ensure_ascii=False))
