"""Exercise deployment failure paths using a fake Docker CLI, without a daemon."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
FAKE_DOCKER = r'''#!/usr/bin/env python3
import gzip, json, os, sys
from pathlib import Path
p=Path(os.environ['FAKE_DOCKER_STATE'])
s=json.loads(p.read_text())
a=sys.argv[1:]
s.setdefault('calls',[]).append(a)
code=0
if a[0]=='compose':
    if 'ps' in a: print(s.get('container','old-server'))
    if 'up' in a and s.get('up_results'): code=s['up_results'].pop(0)
    if 'build' in a: code=s.get('build_result',0)
elif a[0]=='inspect':
    print(s.get('health','healthy') if '.State.Health' in a[2] else 'upic_images' if '.Mounts' in a[2] else 'sha256:old-image')
elif a[:2]==['volume','inspect']: code=0 if s.get('volume_exists',False) else 1
elif a[0]=='run' and '-czf' in a:
    code=s.get('backup_result',0)
    if not code: sys.stdout.buffer.write(gzip.compress(b'backup fixture'))
p.write_text(json.dumps(s))
sys.exit(code)
'''


class OperationsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        shutil.copytree(ROOT / 'scripts', self.root / 'scripts')
        for name in ['.env.example', 'compose.yaml']:
            shutil.copy(ROOT / name, self.root / name)
        bin_dir = self.root / 'fake-bin'
        bin_dir.mkdir()
        docker = bin_dir / 'docker'
        docker.write_text(FAKE_DOCKER)
        docker.chmod(0o755)
        self.state = self.root / 'docker-state.json'
        self.state.write_text('{}')
        self.env = {**os.environ, 'PATH': str(bin_dir) + os.pathsep + os.environ['PATH'],
                    'FAKE_DOCKER_STATE': str(self.state)}
        self.env.pop('UPIC_IMAGE', None)
        self.run_script('init.sh')

    def run_script(self, name, *args, ok=True):
        result = subprocess.run(['bash', str(self.root / 'scripts' / name), *args],
                                cwd=self.root, env=self.env, capture_output=True, text=True)
        if ok:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def setup_state(self, **kwargs):
        self.state.write_text(json.dumps(kwargs))

    def calls(self):
        return json.loads(self.state.read_text())['calls']

    def test_init_is_idempotent_and_does_not_print_secret(self):
        token = (self.root / '.secrets/upload_token').read_text()
        env = (self.root / '.env').read_text()
        result = self.run_script('init.sh', '--public-url', 'https://new.example.com')
        self.assertEqual(token, (self.root / '.secrets/upload_token').read_text())
        self.assertEqual(env, (self.root / '.env').read_text())
        self.assertNotIn(token.strip(), result.stdout + result.stderr)
        self.assertEqual((self.root / '.secrets').stat().st_mode & 0o777, 0o700)

    def test_init_rejects_url_with_injection_or_credentials(self):
        self.run_script('init.sh', '--public-url', 'https://example.com/$(id)', ok=False)
        self.run_script('init.sh', '--public-url', 'https://token@example.com', ok=False)

    def test_init_refuses_secret_symlink(self):
        token = self.root / '.secrets/upload_token'
        token.unlink()
        token.symlink_to(self.root / '.env')
        self.run_script('init.sh', ok=False)

    def test_success_records_previous_image_and_releases_lock(self):
        self.run_script('deploy.sh')
        self.assertTrue((self.root / '.deploy/current-image').read_text().startswith('upic-server:release-'))
        self.assertTrue((self.root / '.deploy/previous-image').read_text().startswith('upic-server:rollback-'))
        self.assertFalse((self.root / '.deploy/lock').exists())

    def test_failed_release_restores_previous_and_still_exits_nonzero(self):
        self.setup_state(up_results=[1, 0])
        self.run_script('deploy.sh', ok=False)
        self.assertTrue((self.root / '.deploy/current-image').read_text().startswith('upic-server:rollback-'))
        self.assertEqual(sum('up' in a for a in self.calls()), 2)
        self.assertFalse((self.root / '.deploy/lock').exists())

    def test_failed_first_deploy_does_not_claim_a_current_image(self):
        self.setup_state(container='', up_results=[1])
        self.run_script('deploy.sh', ok=False)
        self.assertFalse((self.root / '.deploy/current-image').exists())

    def test_failed_build_does_not_replace_running_container(self):
        self.setup_state(build_result=1)
        self.run_script('deploy.sh', ok=False)
        self.assertFalse(any('up' in a for a in self.calls()))

    def test_interrupted_candidate_is_not_used_as_rollback_baseline(self):
        (self.root / '.deploy/current-image').write_text('upic-server:known-good\n')
        self.setup_state(container='failed-candidate', health='unhealthy', up_results=[1, 0])
        self.run_script('deploy.sh', ok=False)
        self.assertEqual((self.root / '.deploy/current-image').read_text().strip(), 'upic-server:known-good')
        self.assertFalse(any(a[:3] == ['image', 'tag', 'sha256:old-image'] for a in self.calls()))

    def test_unhealthy_untracked_container_is_not_a_rollback_baseline(self):
        self.setup_state(health='unhealthy', up_results=[1, 0])
        self.run_script('deploy.sh', ok=False)
        self.assertEqual(sum('up' in a for a in self.calls()), 1)
        self.assertFalse((self.root / '.deploy/current-image').exists())

    def test_backup_failure_restarts_application_and_removes_partial(self):
        self.setup_state(backup_result=1)
        self.run_script('backup.sh', ok=False)
        calls = self.calls()
        self.assertTrue(any('stop' in a for a in calls))
        self.assertIn(['start', 'old-server'], calls)
        self.assertFalse(any('up' in a for a in calls))
        self.assertEqual(list((self.root / 'backups').iterdir()), [])

    def test_backup_success_is_followed_by_health_checked_restart(self):
        self.run_script('backup.sh')
        self.assertEqual(len(list((self.root / 'backups').glob('*.tar.gz'))), 1)
        self.assertIn(['start', 'old-server'], self.calls())
        self.assertTrue(any('.State.Health' in ' '.join(a) for a in self.calls()))
        self.assertFalse(any('up' in a for a in self.calls()))

    def test_restore_never_overwrites_existing_volume(self):
        archive = self.root / 'backup.tar.gz'
        archive.write_bytes(b'unused')
        self.setup_state(volume_exists=True)
        self.run_script('restore.sh', str(archive), 'existing-volume', ok=False)
        self.assertFalse(any(a[0] == 'run' for a in self.calls()))


if __name__ == '__main__':
    unittest.main()
