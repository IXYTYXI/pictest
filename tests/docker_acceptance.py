"""Real Docker restart, backup, restore and rollback acceptance on disposable volumes."""
import argparse
import base64
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='Already built local service image')
    args = parser.parse_args()
    name = 'upic-verify-' + uuid.uuid4().hex[:10]
    original_volume, restored_volume = name + '-images', name + '-restored'
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    env = {**os.environ, 'COMPOSE_PROJECT_NAME': name, 'DATA_VOLUME_NAME': original_volume,
           'HTTP_PORT': str(port), 'PUBLIC_BASE_URL': base, 'BIND_HOST': '127.0.0.1'}
    env.pop('UPIC_IMAGE', None)
    env.pop('COMPOSE_PROFILES', None)
    with tempfile.TemporaryDirectory(prefix='upic-docker-acceptance-') as tmp:
        root = Path(tmp)
        for folder in ['scripts', 'deploy']:
            shutil.copytree(ROOT / folder, root / folder)
        for filename in ['compose.yaml', '.env.example']:
            shutil.copy(ROOT / filename, root / filename)
        compose = ['docker', 'compose', '--env-file', '.env', '-f', 'compose.yaml']

        def run(argv, capture=False, check=True):
            result = subprocess.run(argv, cwd=root, env=env, check=check, text=True,
                                    stdout=subprocess.PIPE if capture else None)
            return result.stdout.strip() if capture else result

        def request(path, data=None, authenticate=False):
            headers = {'Authorization': 'Bearer ' + token} if authenticate else {}
            if data is not None:
                headers['Content-Type'] = 'application/json'
            req = urllib.request.Request(base + path, data=data, headers=headers)
            with urllib.request.urlopen(req, timeout=15) as response:
                return response.read()

        def ready():
            for _ in range(90):
                try:
                    request('/readyz')
                    return
                except OSError:
                    time.sleep(1)
            raise RuntimeError('Application did not become ready')

        def verify_image(label):
            if request(public_path) != image:
                raise RuntimeError('Image changed: ' + label)
            print('PASS ' + label, flush=True)

        created_images = set()
        try:
            run(['bash', 'scripts/init.sh', '--public-url', base])
            token = (root / '.secrets/upload_token').read_text().strip()
            run(['bash', 'scripts/deploy.sh', '--image', args.image])
            ready()
            first_release = (root / '.deploy/current-image').read_text().strip()
            created_images.add(first_release)
            spec = importlib.util.spec_from_file_location('smoke', ROOT / 'scripts/smoke-test.py')
            smoke = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(smoke)
            image = smoke.png()
            payload = json.dumps({'file': base64.b64encode(image).decode(), 'filename': 'persistence-test.png'}).encode()
            metadata = json.loads(request('/api/upload', payload, True))['data']
            public_path = '/i/' + metadata['id'] + '.png'
            verify_image('initial image upload')
            run(compose + ['restart', 'server'])
            ready()
            verify_image('data survives container restart')
            original_container = run(compose + ['ps', '--quiet', 'server'], capture=True)
            run(['bash', 'scripts/backup.sh'])
            if run(compose + ['ps', '--quiet', 'server'], capture=True) != original_container:
                raise RuntimeError('Backup replaced the original container')
            verify_image('backup preserves container and image data')
            archive = next((root / 'backups').glob('*.tar.gz'))
            run(['bash', 'scripts/restore.sh', str(archive), restored_volume])
            env['DATA_VOLUME_NAME'] = restored_volume
            run(['bash', 'scripts/deploy.sh', '--image', first_release])
            created_images.add((root / '.deploy/current-image').read_text().strip())
            verify_image('restored volume serves original bytes')
            run(['bash', 'scripts/rollback.sh'])
            created_images.add((root / '.deploy/current-image').read_text().strip())
            verify_image('image rollback preserves restored data')
            run(['python3', 'scripts/smoke-test.py', '--url', base, '--token-file', '.secrets/upload_token'])
            print('PASS real Docker acceptance (restart / backup / restore / rollback / API)', flush=True)
        finally:
            if (root / '.env').exists():
                run(compose + ['down', '--remove-orphans'], check=False)
            for volume in [original_volume, restored_volume]:
                run(['docker', 'volume', 'rm', volume], check=False)
            for release in created_images:
                run(['docker', 'image', 'rm', release], check=False)


if __name__ == '__main__':
    main()
