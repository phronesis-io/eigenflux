import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class WindowsInstallation(unittest.TestCase):
    def test_current_binary_is_used_for_migration_and_registration(self):
        shell = shutil.which('powershell') or shutil.which('pwsh')
        if os.name != 'nt' or not shell:
            self.skipTest('Windows PowerShell required')
        source = (Path(__file__).resolve().parents[2] / 'static/install.ps1').read_text(encoding='utf-8')
        prefix = source.split('# ── Main ─')[0]
        self.assertNotEqual(prefix, source)
        with tempfile.TemporaryDirectory(prefix='existing CLI ') as temp:
            root = Path(temp)
            binary = root / 'eigenflux.exe'
            log = root / 'calls.txt'
            stub = root / 'main.go'
            stub.write_text('''package main
import ("fmt"; "os"; "strings")
func main() {
    if len(os.Args) == 3 && os.Args[1] == "version" {
        fmt.Println("99.0.0")
        return
    }
    if strings.Contains(strings.Join(os.Args[1:], "|"), "skills|path") { fmt.Println(os.Getenv("TEST_SKILLS")); return }
    f, err := os.OpenFile(os.Getenv("TEST_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
    if err != nil { panic(err) }
    defer f.Close()
    fmt.Fprintln(f, strings.Join(os.Args[1:], "|"))
}
''', encoding='utf-8')
            subprocess.run(['go', 'build', '-o', str(binary), str(stub)], check=True,
                           env=dict(os.environ, GO111MODULE='off'), capture_output=True,
                           text=True, timeout=60)
            fixture = root / 'fixture.ps1'
            fixture.write_text(prefix + """
function Invoke-RestMethod { '99.0.0' }
function Get-InvokingHost { 'codex' }
Install-Cli
if ($script:installedCliPath -ne $env:TEST_BINARY) { throw 'Wrong existing CLI path' }
Migrate-Config
Install-Skills
& $script:installedCliPath --homedir $env:EIGENFLUX_HOME installation record --host codex "--skills-target=$($script:installedSkillsTarget)"
if ($LASTEXITCODE -ne 0) { throw 'Registration failed' }
""", encoding='utf-8-sig')
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                       TEST_CALLS=str(log), TEST_BINARY=str(binary), TEST_SKILLS=str(root / ".agents" / "skills"),
                       EIGENFLUX_HOME=str(root / 'agent home'), USERPROFILE=str(root))
            result = subprocess.run([shell, '-NoProfile', '-ExecutionPolicy', 'Bypass',
                                     '-File', str(fixture)], env=env, capture_output=True,
                                    text=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            calls = log.read_text()
            self.assertIn('|migrate', calls)
            self.assertIn('|skills|sync|--host=codex', calls)
            self.assertIn('|installation|record|--host|codex|--skills-target=' + str(root / '.agents' / 'skills'), calls)

    def test_notification_failure_does_not_skip_watch_recovery(self):
        shell = shutil.which('powershell') or shutil.which('pwsh')
        if os.name != 'nt' or not shell:
            self.skipTest('Windows PowerShell required')
        source = (Path(__file__).resolve().parents[2] / 'static/install.ps1').read_text(encoding='utf-8')
        prefix, main = source.split('# ── Main ─', 1)
        main = main[main.index('\n'):]
        with tempfile.TemporaryDirectory(prefix='notification recovery ') as temp:
            root = Path(temp)
            binary = root / 'eigenflux.exe'
            log = root / 'calls.txt'
            stub = root / 'main.go'
            stub.write_text('''package main
import ("fmt"; "os"; "strings")
func main() {
    args := strings.Join(os.Args[1:], "|")
    f, err := os.OpenFile(os.Getenv("TEST_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
    if err != nil { panic(err) }
    fmt.Fprintln(f, args)
    f.Close()
    if strings.Contains(args, "notify|" + os.Getenv("TEST_NOTIFY_FAILURE")) && os.Getenv("TEST_NOTIFY_FAILURE") != "" { os.Exit(1) }
}
''', encoding='utf-8')
            subprocess.run(['go', 'build', '-o', str(binary), str(stub)], check=True,
                           env=dict(os.environ, GO111MODULE='off'), capture_output=True,
                           text=True, timeout=60)
            fixture = root / 'fixture.ps1'
            fixture.write_text(prefix + '''
function Install-Cli { $script:installedCliPath = $env:TEST_BINARY }
function Install-Skills { $script:installedSkillsTarget = $env:TEST_SKILLS }
function Migrate-Config {}
function Setup-Agents {}
function Get-InvokingHost { 'workbuddy' }
''' + main, encoding='utf-8-sig')
            for failure, skip_agent in [('setup', False), ('status', False), ('', False), ('setup', True)]:
                with self.subTest(notification_failure=failure, skip_agent=skip_agent):
                    if log.exists():
                        log.unlink()
                    env = dict(os.environ, TEST_CALLS=str(log), TEST_BINARY=str(binary),
                               TEST_SKILLS=str(root / '.agents' / 'skills'),
                               EIGENFLUX_HOME=str(root / 'agent home'), USERPROFILE=str(root),
                               TEST_NOTIFY_FAILURE=failure, EIGENFLUX_SKIP_AGENT_SETUP='1' if skip_agent else '0')
                    result = subprocess.run([shell, '-NoProfile', '-ExecutionPolicy', 'Bypass',
                                             '-File', str(fixture)], env=env, capture_output=True,
                                            text=True, timeout=30)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    calls = log.read_text()
                    self.assertIn('|notify|setup|--source|installer', calls)
                    if skip_agent:
                        self.assertNotIn('watch|recover', calls)
                    else:
                        self.assertIn('|watch|recover', calls)
                    if failure:
                        self.assertIn('notifications are not ready', result.stdout)
                        self.assertNotIn('Windows notification registration is complete', result.stdout)
                    else:
                        self.assertIn('Windows notification registration is complete', result.stdout)
                        self.assertNotIn('notifications are not ready', result.stdout)
