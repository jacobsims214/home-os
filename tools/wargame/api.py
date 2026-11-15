#!/usr/bin/env python3
"""REST call to the Home OS API as one household member.

usage: api.py <METHOD> <path> ['<json-body>']     e.g. api.py GET /api/v1/auth/me

Configuration (env vars):
  HOMEOS_API_URL        API base URL, e.g. http://home-os-api.home-os:8080
  Auth: same as mcpcall.py (HOMEOS_AGENT_TOKEN, or HOMEOS_DEX_TOKEN_URL + HOMEOS_USER + HOMEOS_PASSWORD)
"""
import os
import sys
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mcpcall import bearer, env  # noqa: E402

if len(sys.argv) < 3:
    sys.exit(__doc__)
method, path = sys.argv[1:3]
body = sys.argv[3].encode() if len(sys.argv) > 3 else None
req = urllib.request.Request(env("HOMEOS_API_URL").rstrip("/") + path, body, method=method,
                             headers={"Authorization": "Bearer " + bearer(), "Content-Type": "application/json"})
try:
    r = urllib.request.urlopen(req, timeout=30)
    print(r.status, r.read().decode()[:2000])
except urllib.error.HTTPError as e:
    print(e.code, e.read().decode()[:2000])
