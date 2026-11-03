"""Exercise fail-closed acceptance under normal and optimized Python, offline."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[2] / 'cli/scripts/verify-published-bridge.py'
DRIVER = r'''
import importlib.util,sys,json,hashlib,subprocess
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('bridge',sys.argv[1]);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
report=Path(sys.argv[2]);scenario=sys.argv[3];calls=[]
binary=b'fixture-not-executed'
def fetch(path):
    if path.endswith('release.json'):
        version='1.0.7' if '/v2/' in path else '1.0.5'
        return json.dumps({'version':version,'artifacts':{'eigenflux-darwin-arm64':{'size':len(binary),'sha256':hashlib.sha256(binary).hexdigest() if scenario!='bad-hash' else 'bad'}}}).encode()
    return binary
def run(*args,**kwargs):
    calls.append(args[0])
    if scenario in ('eligible-wrong','missing-receipts'):
        Path(args[0][0]+'.update.json').write_text('{}')
    if scenario=='credentials-changed':
        home=Path(args[0][args[0].index('--homedir')+1])
        (home/'servers/audit/agent-v2-credentials.json').write_text('changed')
    return subprocess.CompletedProcess(args[0],2,'','intentional missing Skills')
def version(*args,**kwargs):
    if scenario=='wrong-version' or (scenario=='eligible-wrong' and len(calls)==3):return '0.0.0'
    return '1.0.7' if scenario=='missing-receipts' and len(calls)==3 else '1.0.5'
class Server:
    server_port=12345
    def serve_forever(self):pass
    def shutdown(self):pass
    def server_close(self):pass
with patch.object(sys,'argv',[str(module.__file__),'--output',str(report)]),patch.object(module,'fetch',fetch),patch.object(module.platform,'system',return_value='Darwin'),patch.object(module.platform,'machine',return_value='arm64'),patch.object(module.http.server,'ThreadingHTTPServer',return_value=Server()),patch.object(module.subprocess,'run',run),patch.object(module.subprocess,'check_output',version):
    try:module.main()
    except RuntimeError as error:print(json.dumps({'error':str(error),'calls':len(calls)}))
    else:print(json.dumps({'error':None,'calls':len(calls)}))
'''


class AcceptanceGuardTest(unittest.TestCase):
    def test_optimization_cannot_skip_checks_or_report_a_false_pass(self):
        for optimized in (False, True):
            for scenario, error, calls in (
                ('bad-hash', 'checksum or size mismatch', 0),
                ('wrong-version', 'first cycle ran 0.0.0', 1),
                ('missing-state', 'no updater eligibility state', 2),
                ('eligible-wrong', 'eligible cycle ran 0.0.0', 3),
                ('missing-receipts', 'missing automatic execution receipt', 3),
                ('credentials-changed', 'credentials changed', 1),
            ):
                with self.subTest(optimized=optimized, scenario=scenario), tempfile.TemporaryDirectory() as temporary:
                    report=Path(temporary)/'report.json'
                    env={k:v for k,v in os.environ.items() if k!='PYTHONOPTIMIZE'}
                    command=[sys.executable]+(['-O'] if optimized else [])+['-c',DRIVER,str(SCRIPT),str(report),scenario]
                    result=subprocess.run(command,capture_output=True,text=True,env=env,timeout=15)
                    self.assertEqual(result.returncode,0,result.stderr)
                    outcome=json.loads(result.stdout.splitlines()[-1])
                    self.assertIn(error,outcome['error'])
                    self.assertEqual(outcome['calls'],calls)
                    if report.exists():self.assertIs(json.loads(report.read_text())['passed'],False)
