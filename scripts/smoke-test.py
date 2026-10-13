#!/usr/bin/env python3
"""Upload two temporary images, verify access, then delete only those images."""
import argparse
import base64
import json
from pathlib import Path
import struct
import urllib.error
import urllib.request
import uuid
import zlib


def png():
    def chunk(name, payload):
        return struct.pack('!I', len(payload)) + name + payload + struct.pack('!I', zlib.crc32(name + payload))
    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('!2I5B', 1, 1, 8, 2, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(b'\x00\x24\x88\x75')) + chunk(b'IEND', b''))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', required=True)
    parser.add_argument('--token-file', required=True)
    args = parser.parse_args()
    base = args.url.rstrip('/')
    token = Path(args.token_file).read_text().strip()
    created = []

    def request(path, method='GET', data=None, content_type=None, auth=True):
        headers = {'Authorization': 'Bearer ' + token} if auth else {}
        if content_type:
            headers['Content-Type'] = content_type
        req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
        with urllib.request.urlopen(req, timeout=30) as response:
            return response.status, response.read(), response.headers

    def check(condition, label):
        if not condition:
            raise RuntimeError('Smoke check failed: ' + label)
        print('PASS ' + label)

    try:
        check(request('/readyz', auth=False)[0] == 200, 'storage readiness')
        try:
            request('/api/images', auth=False)
            raise RuntimeError('Unauthenticated listing was allowed')
        except urllib.error.HTTPError as error:
            check(error.code == 401, 'authentication required')
        image = png()
        boundary = uuid.uuid4().hex
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="smoke.png"\r\n'
                'Content-Type: image/png\r\n\r\n').encode() + image + f'\r\n--{boundary}--\r\n'.encode()
        code, payload, _ = request('/api/upload', 'POST', body, 'multipart/form-data; boundary=' + boundary)
        item = json.loads(payload)['data']
        created.append(item['id'])
        check(code == 201 and item['width'] == 1, 'multipart upload')
        public_path = '/i/' + item['id'] + '.png'
        code, downloaded, headers = request(public_path, auth=False)
        check(code == 200 and downloaded == image and headers['Content-Type'] == 'image/png', 'public image bytes')
        check(request(public_path, 'HEAD', auth=False)[0] == 200, 'public HEAD')
        code, payload, _ = request('/api/upload', 'POST', json.dumps({'file': base64.b64encode(image).decode(), 'filename': 'smoke-json.png'}).encode(), 'application/json')
        second = json.loads(payload)['data']
        created.append(second['id'])
        check(code == 201 and second['url'].startswith(('https://', 'http://')), 'uPic JSON Base64 upload')
        listing = json.loads(request('/api/images?limit=100')[1])['data']
        check(all(image_id in {entry['id'] for entry in listing['items']} for image_id in created), 'gallery listing')
        for image_id in created[:]:
            check(request('/api/images/' + image_id, 'DELETE')[0] == 200, 'delete uploaded test image')
            created.remove(image_id)
        try:
            request(public_path, auth=False)
            raise RuntimeError('Deleted image is still served')
        except urllib.error.HTTPError as error:
            check(error.code == 404, 'deleted image unavailable')
    finally:
        for image_id in created:
            request('/api/images/' + image_id, 'DELETE')


if __name__ == '__main__':
    main()
