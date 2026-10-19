#!/usr/bin/env python3
"""Print WAHA operations matching a regex, with their parameters, bodies and
responses, from the pinned OpenAPI. Usage: scripts/waha-op.py 'POST /api/sendText$'"""
import json,sys,re
import os
d=json.load(open(os.path.join(os.path.dirname(__file__), '..', 'contract', 'waha-openapi.json')))
pat=re.compile(sys.argv[1])
S=d['components']['schemas']
def ref(r): return r.split('/')[-1]
def show(schema,depth=0,seen=()):
    if schema is None: return 'None'
    if '$ref' in schema:
        n=ref(schema['$ref'])
        if n in seen or depth>2: return n
        return n+':'+show(S[n],depth+1,seen+(n,))
    t=schema.get('type')
    if 'enum' in schema: return 'enum'+str(schema['enum'])
    if t=='array': return '['+show(schema.get('items'),depth,seen)+']'
    if t=='object' or 'properties' in schema:
        req=set(schema.get('required',[]))
        props=schema.get('properties',{})
        return '{'+', '.join(f"{k}{'' if k in req else '?'}:{show(v,depth+1,seen)}" for k,v in props.items())+'}'
    for k in ('oneOf','anyOf','allOf'):
        if k in schema: return k+'('+' | '.join(show(x,depth,seen) for x in schema[k])+')'
    return t or '?'
for p,ops in d['paths'].items():
    for m,o in ops.items():
        if m not in ('get','post','put','delete'): continue
        line=f"{m.upper()} {p}"
        if not pat.search(line): continue
        print('==',line,'|',o.get('summary',''))
        for prm in o.get('parameters',[]):
            print('   param',prm['in'],prm['name'],'' if prm.get('required') else '?',show(prm.get('schema')))
        rb=o.get('requestBody',{}).get('content',{})
        for ct,c in rb.items(): print('   body',ct,show(c.get('schema')))
        for code,r in o.get('responses',{}).items():
            for ct,c in r.get('content',{}).items(): print('   resp',code,show(c.get('schema')))
