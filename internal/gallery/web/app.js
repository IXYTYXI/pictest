(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const allowedFormats = ['image/png', 'image/jpeg', 'image/gif'];
  const formatNames = { 'image/png': 'PNG', 'image/jpeg': 'JPEG', 'image/gif': 'GIF' };
  const pageSize = 24;
  const requests = new Set();
  const deleting = new Set();
  let token = '';
  let connected = false;
  let connecting = false;
  let generation = 0;
  let listRequest = 0;
  let offset = 0;
  let total = 0;
  let maxUploadBytes = 10 * 1024 * 1024;
  let formats = [...allowedFormats];
  let queue = [];
  let queueRunning = false;
  let activeUpload = null;
  let pendingDelete = null;
  let noticeTimer = null;
  let dragDepth = 0;

  function formatBytes(value) {
    const bytes = Number(value) || 0;
    if (bytes <= 0) return '0 B';
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: index > 0 ? 1 : 0 }).format(bytes / (1024 ** index))} ${units[index]}`;
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function notify(message, isError = false) {
    clearTimeout(noticeTimer);
    $('notice').textContent = message;
    $('notice').className = isError ? 'notice is-error' : 'notice';
    noticeTimer = setTimeout(() => {
      $('notice').className = 'notice is-empty';
      $('notice').textContent = '';
    }, isError ? 7000 : 4000);
  }

  function apiError(body, status) {
    const error = new Error(body?.error?.message || (status ? `请求未成功（${status}），请稍后重试。` : '网络连接失败，请检查连接后重试。'));
    error.status = status;
    return error;
  }

  async function request(path, method = 'GET') {
    const controller = new AbortController();
    requests.add(controller);
    try {
      const response = await fetch(path, {
        method,
        headers: { Authorization: `Bearer ${token}` },
        credentials: 'omit',
        cache: 'no-store',
        signal: controller.signal,
      });
      const body = await response.json().catch(() => null);
      if (!response.ok || !body?.success) throw apiError(body, response.status);
      return body.data;
    } catch (error) {
      if (error.name === 'TypeError') throw apiError(null, 0);
      throw error;
    } finally {
      requests.delete(controller);
    }
  }

  function updateConnection() {
    $('connectionStatusText').textContent = connected ? '已连接' : connecting ? '连接中' : '未连接';
    $('connectionStatus').className = `status-pill${connected ? ' is-connected' : connecting ? ' is-connecting' : ''}`;
    $('disconnectButton').hidden = !connected && !connecting;
    $('disconnectButton').textContent = connecting ? '取消连接' : '断开连接';
    $('connectButton').disabled = connecting;
    $('connectButton').textContent = connecting ? '正在连接…' : '连接图库 ↗';
    $('tokenInput').disabled = connecting;
    $('connectionPanel').hidden = connected;
    $('workspace').hidden = !connected;
  }

  function disconnect(message = '', focus = true) {
    generation += 1;
    listRequest += 1;
    token = '';
    connected = false;
    connecting = false;
    requests.forEach((controller) => controller.abort());
    requests.clear();
    if (activeUpload) activeUpload.abort();
    activeUpload = null;
    queue = [];
    queueRunning = false;
    offset = 0;
    total = 0;
    deleting.clear();
    pendingDelete = null;
    dragDepth = 0;
    $('dropZone').classList.remove('is-dragging');
    $('tokenInput').value = '';
    $('fileInput').value = '';
    $('galleryGrid').replaceChildren();
    $('uploadQueue').replaceChildren();
    $('queueSection').hidden = true;
    $('connectionMessage').textContent = message;
    $('copyText').value = '';
    $('deleteFilename').textContent = '';
    for (const id of ['deleteDialog', 'copyDialog']) {
      if ($(id).open) $(id).close();
    }
    updateConnection();
    if (focus) $('tokenInput').focus();
  }

  function handleError(error, operationGeneration) {
    if (operationGeneration !== generation || error.name === 'AbortError') return;
    if (error.status === 401) {
      disconnect('访问令牌无效或已失效，请重新输入后连接。');
      return;
    }
    notify(error.message || '操作失败，请稍后重试。', true);
  }

  function updateStats(data) {
    total = Number(data.total) || 0;
    $('totalCount').replaceChildren(document.createTextNode(total.toLocaleString('zh-CN')), element('span', '', '张'));
    $('galleryCount').textContent = total.toLocaleString('zh-CN');
    $('usedStorage').textContent = formatBytes(data.bytes);
    const capacity = Number(data.max_bytes) || 0;
    $('totalStorage').textContent = capacity > 0 ? formatBytes(capacity) : '未设上限';
    $('storageProgress').hidden = capacity <= 0;
    $('storageProgress').value = capacity > 0 ? Math.min(100, ((Number(data.bytes) || 0) / capacity) * 100) : 0;
  }

  function safeImageURL(value) {
    try {
      const url = new URL(value, window.location.origin);
      return ['http:', 'https:'].includes(url.protocol) ? url.href : null;
    } catch {
      return null;
    }
  }

  async function copy(text) {
    const operationGeneration = generation;
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(text);
      if (operationGeneration === generation && connected) notify('已复制到剪贴板');
    } catch {
      if (operationGeneration !== generation || !connected) return;
      $('copyText').value = text;
      if (!$('copyDialog').open) $('copyDialog').showModal();
      $('copyText').focus();
      $('copyText').select();
    }
  }

  function markdownFor(item, url) {
    const label = String(item.filename || '图片').replace(/[\r\n]/g, ' ').replace(/[\\\[\]]/g, '\\$&');
    return `![${label}](${url.replace(/\(/g, '%28').replace(/\)/g, '%29')})`;
  }

  function cardFor(item) {
    const card = element('article', 'image-card');
    const url = safeImageURL(item.url);
    const preview = element(url ? 'a' : 'div', 'image-preview');
    if (url) {
      preview.href = url;
      preview.target = '_blank';
      preview.rel = 'noopener noreferrer';
      preview.setAttribute('aria-label', `在新标签页查看 ${item.filename || '图片'}`);
      const image = element('img');
      image.src = url;
      image.alt = String(item.filename || '图片');
      image.loading = 'lazy';
      image.decoding = 'async';
      preview.append(image);
    }
    const details = element('div', 'image-details');
    const title = element('h3', 'image-title', item.filename || '未命名图片');
    title.title = String(item.filename || '未命名图片');
    const meta = element('p', 'image-meta');
    if (item.width && item.height) meta.append(element('span', '', `${item.width} × ${item.height}`));
    meta.append(element('span', '', formatBytes(item.size)));
    if (formatNames[item.mime]) meta.append(element('span', '', formatNames[item.mime]));
    const actions = element('div', 'image-actions');
    const copyURL = element('button', 'button button-secondary', '复制链接');
    const copyMarkdown = element('button', 'button button-secondary', 'Markdown');
    const remove = element('button', 'button button-quiet delete-button', deleting.has(item.id) ? '删除中…' : '删除');
    for (const button of [copyURL, copyMarkdown, remove]) button.type = 'button';
    copyURL.disabled = !url;
    copyMarkdown.disabled = !url;
    copyURL.setAttribute('aria-label', `复制 ${item.filename || '图片'} 的链接`);
    copyMarkdown.setAttribute('aria-label', `复制 ${item.filename || '图片'} 的 Markdown`);
    remove.setAttribute('aria-label', `删除 ${item.filename || '图片'}`);
    remove.disabled = deleting.has(item.id);
    copyURL.addEventListener('click', () => copy(url));
    copyMarkdown.addEventListener('click', () => copy(markdownFor(item, url)));
    remove.addEventListener('click', () => {
      pendingDelete = { item, button: remove };
      $('deleteFilename').textContent = String(item.filename || '未命名图片');
      $('deleteDialog').showModal();
      $('cancelDeleteButton').focus();
    });
    actions.append(copyURL, copyMarkdown, remove);
    details.append(title, meta, actions);
    card.append(preview, details);
    return card;
  }

  function displayGallery(data) {
    const items = Array.isArray(data.items) ? data.items : [];
    updateStats(data);
    $('galleryGrid').replaceChildren(...items.map(cardFor));
    $('galleryEmpty').hidden = items.length !== 0;
    $('pagination').hidden = total <= pageSize;
    $('pageInfo').textContent = total ? `第 ${offset + 1}–${Math.min(offset + items.length, total)} 张，共 ${total} 张` : '';
    $('previousButton').disabled = offset === 0;
    $('nextButton').disabled = offset + pageSize >= total;
  }

  async function loadGallery(nextOffset = offset) {
    if (!connected) return;
    const operationGeneration = generation;
    const requestNumber = ++listRequest;
    $('galleryGrid').setAttribute('aria-busy', 'true');
    $('galleryLoading').hidden = false;
    $('galleryError').hidden = true;
    $('galleryEmpty').hidden = true;
    $('refreshButton').disabled = true;
    $('previousButton').disabled = true;
    $('nextButton').disabled = true;
    try {
      const data = await request(`/api/images?offset=${Math.max(0, nextOffset)}&limit=${pageSize}`);
      if (operationGeneration !== generation || requestNumber !== listRequest) return;
      if (nextOffset > 0 && nextOffset >= data.total) {
        return loadGallery(Math.max(0, Math.ceil(data.total / pageSize) - 1) * pageSize);
      }
      offset = Math.max(0, nextOffset);
      displayGallery(data);
    } catch (error) {
      if (operationGeneration !== generation || requestNumber !== listRequest || error.name === 'AbortError') return;
      if (error.status === 401) {
        handleError(error, operationGeneration);
      } else {
        $('galleryError').textContent = `${error.message} 点击「刷新图库」重试。`;
        $('galleryError').hidden = false;
      }
    } finally {
      if (operationGeneration === generation && requestNumber === listRequest) {
        $('galleryGrid').setAttribute('aria-busy', 'false');
        $('galleryLoading').hidden = true;
        $('refreshButton').disabled = false;
        $('previousButton').disabled = offset === 0;
        $('nextButton').disabled = offset + pageSize >= total;
      }
    }
  }

  function updateQueueItem(entry, status, message) {
    entry.status = status;
    entry.node.className = `queue-item is-${status}`;
    entry.statusNode.textContent = message;
    entry.progress.hidden = status !== 'uploading';
    updateQueueSummary();
  }

  function updateQueueSummary() {
    const waiting = queue.filter((entry) => entry.status === 'waiting').length;
    const uploading = queue.some((entry) => entry.status === 'uploading');
    const done = queue.filter((entry) => entry.status === 'done').length;
    const failed = queue.filter((entry) => entry.status === 'error').length;
    $('queueSection').hidden = queue.length === 0;
    $('queueSummary').textContent = [uploading ? '正在上传' : '', waiting ? `${waiting} 张等待中` : '', done ? `${done} 张上传成功` : '', failed ? `${failed} 张未上传` : ''].filter(Boolean).join(' · ');
    $('clearQueueButton').disabled = !done && !failed;
  }

  function addFiles(files) {
    if (!connected) {
      notify('请先连接图库，再上传图片。', true);
      return;
    }
    for (const file of files) {
      const node = element('li', 'queue-item');
      const info = element('div');
      const title = element('p', 'queue-name', file.name || '粘贴的图片');
      title.title = file.name || '粘贴的图片';
      const progress = element('progress');
      progress.max = 100;
      progress.value = 0;
      progress.hidden = true;
      progress.setAttribute('aria-label', `${file.name || '图片'} 的上传进度`);
      const statusNode = element('span', 'queue-status');
      info.append(title, progress);
      node.append(element('span', 'queue-symbol', '▧'), info, statusNode);
      const entry = { file, node, progress, statusNode, status: 'waiting' };
      queue.push(entry);
      $('uploadQueue').append(node);
      if (!formats.includes(file.type)) {
        updateQueueItem(entry, 'error', `不支持此格式，请使用 ${formats.map((type) => formatNames[type]).join('、')}`);
        entry.file = null;
      } else if (file.size <= 0) {
        updateQueueItem(entry, 'error', '图片为空，请重新选择');
        entry.file = null;
      } else if (file.size > maxUploadBytes) {
        updateQueueItem(entry, 'error', `超过单张 ${formatBytes(maxUploadBytes)} 的限制`);
        entry.file = null;
      } else {
        updateQueueItem(entry, 'waiting', `等待上传 · ${formatBytes(file.size)}`);
      }
    }
    updateQueueSummary();
    runQueue();
  }

  function upload(entry) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      activeUpload = xhr;
      xhr.open('POST', '/api/upload');
      xhr.setRequestHeader('Authorization', `Bearer ${token}`);
      xhr.timeout = 120000;
      xhr.upload.addEventListener('progress', (event) => {
        if (event.lengthComputable) {
          const percent = Math.min(100, Math.round(event.loaded / event.total * 100));
          entry.progress.value = percent;
          entry.statusNode.textContent = percent === 100 ? '正在保存…' : `正在上传 ${percent}%`;
        }
      });
      xhr.addEventListener('load', () => {
        let body;
        try { body = JSON.parse(xhr.responseText); } catch { body = null; }
        if (xhr.status >= 200 && xhr.status < 300 && body?.success) resolve(body.data);
        else reject(apiError(body, xhr.status));
      });
      xhr.addEventListener('error', () => reject(apiError(null, 0)));
      xhr.addEventListener('timeout', () => reject(new Error('上传超时，请重新选择图片上传。')));
      xhr.addEventListener('abort', () => reject(new DOMException('已取消上传', 'AbortError')));
      xhr.addEventListener('loadend', () => { if (activeUpload === xhr) activeUpload = null; });
      const form = new FormData();
      form.append('file', entry.file, entry.file.name || '粘贴的图片');
      xhr.send(form);
    });
  }

  async function runQueue() {
    if (queueRunning || !connected) return;
    queueRunning = true;
    const operationGeneration = generation;
    let uploaded = false;
    try {
      while (connected && operationGeneration === generation) {
        const entry = queue.find((candidate) => candidate.status === 'waiting');
        if (!entry) break;
        updateQueueItem(entry, 'uploading', '正在上传 0%');
        try {
          await upload(entry);
          if (operationGeneration !== generation) return;
          uploaded = true;
          updateQueueItem(entry, 'done', '上传成功 ✓');
        } catch (error) {
          if (operationGeneration !== generation) return;
          if (error.status === 401) {
            handleError(error, operationGeneration);
            return;
          }
          updateQueueItem(entry, 'error', error.name === 'AbortError' ? '已取消' : error.message);
        } finally {
          entry.file = null;
        }
      }
    } finally {
      if (operationGeneration === generation) {
        queueRunning = false;
        updateQueueSummary();
        if (uploaded) {
          notify('上传完成，图片已加入图库');
          loadGallery(0);
        }
      }
    }
  }

  async function readConfig() {
    const controller = new AbortController();
    requests.add(controller);
    try {
      const response = await fetch('/api/config', { credentials: 'omit', cache: 'no-store', signal: controller.signal });
      const result = await response.json();
      if (!response.ok || !result.success) return;
      const data = result.data;
      if (Number.isSafeInteger(data.max_upload_bytes) && data.max_upload_bytes > 0) maxUploadBytes = data.max_upload_bytes;
      const advertised = Array.isArray(data.formats) ? data.formats.filter((type) => allowedFormats.includes(type)) : [];
      if (advertised.length) formats = advertised;
      $('fileInput').accept = formats.join(',');
      $('uploadHint').textContent = `支持 ${formats.map((type) => formatNames[type]).join('、')}，单张不超过 ${formatBytes(maxUploadBytes)}`;
    } catch {
      // Defaults remain available when configuration cannot be loaded.
    } finally {
      requests.delete(controller);
    }
  }

  $('connectForm').addEventListener('submit', async (event) => {
    event.preventDefault();
    if (connecting) return;
    const candidate = $('tokenInput').value.trim();
    if (!candidate) {
      $('connectionMessage').textContent = '请输入访问令牌。';
      $('tokenInput').focus();
      return;
    }
    token = candidate;
    $('tokenInput').value = '';
    $('connectionMessage').textContent = '';
    connecting = true;
    const operationGeneration = ++generation;
    updateConnection();
    try {
      const data = await request(`/api/images?offset=0&limit=${pageSize}`);
      if (operationGeneration !== generation) return;
      connected = true;
      connecting = false;
      offset = 0;
      updateConnection();
      $('galleryError').hidden = true;
      $('galleryLoading').hidden = true;
      $('refreshButton').disabled = false;
      displayGallery(data);
      $('chooseFilesButton').focus();
      notify('已连接图库');
    } catch (error) {
      if (operationGeneration !== generation || error.name === 'AbortError') return;
      disconnect(error.status === 401 ? '访问令牌无效或已失效，请重新输入后连接。' : `连接失败：${error.message}`);
    }
  });

  $('disconnectButton').addEventListener('click', () => {
    disconnect();
    notify('已断开连接，访问令牌已清除');
  });
  $('chooseFilesButton').addEventListener('click', () => $('fileInput').click());
  $('fileInput').addEventListener('change', (event) => {
    addFiles(Array.from(event.target.files || []));
    event.target.value = '';
  });
  $('refreshButton').addEventListener('click', () => loadGallery());
  $('previousButton').addEventListener('click', () => loadGallery(offset - pageSize));
  $('nextButton').addEventListener('click', () => loadGallery(offset + pageSize));
  $('clearQueueButton').addEventListener('click', () => {
    queue = queue.filter((entry) => {
      if (entry.status === 'waiting' || entry.status === 'uploading') return true;
      entry.node.remove();
      return false;
    });
    updateQueueSummary();
  });

  $('dropZone').addEventListener('dragenter', (event) => {
    event.preventDefault();
    dragDepth += 1;
    $('dropZone').classList.add('is-dragging');
  });
  $('dropZone').addEventListener('dragover', (event) => {
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy';
  });
  $('dropZone').addEventListener('dragleave', () => {
    dragDepth = Math.max(0, dragDepth - 1);
    if (!dragDepth) $('dropZone').classList.remove('is-dragging');
  });
  $('dropZone').addEventListener('drop', (event) => {
    event.preventDefault();
    dragDepth = 0;
    $('dropZone').classList.remove('is-dragging');
    addFiles(Array.from(event.dataTransfer?.files || []));
  });
  document.addEventListener('dragover', (event) => {
    if (event.dataTransfer?.types.includes('Files')) event.preventDefault();
  });
  document.addEventListener('drop', (event) => {
    if (event.dataTransfer?.types.includes('Files')) event.preventDefault();
  });
  document.addEventListener('paste', (event) => {
    if (!connected || event.target.closest('input, textarea, [contenteditable="true"]') || $('copyDialog').open || $('deleteDialog').open) return;
    const files = Array.from(event.clipboardData?.items || []).filter((item) => item.kind === 'file').map((item) => item.getAsFile()).filter(Boolean);
    if (files.length) {
      event.preventDefault();
      addFiles(files);
    }
  });

  $('cancelDeleteButton').addEventListener('click', () => $('deleteDialog').close());
  $('deleteDialog').addEventListener('close', () => { pendingDelete = null; });
  $('confirmDeleteButton').addEventListener('click', async () => {
    if (!pendingDelete || !connected) return;
    const { item, button } = pendingDelete;
    const operationGeneration = generation;
    $('deleteDialog').close();
    deleting.add(item.id);
    button.disabled = true;
    button.textContent = '删除中…';
    try {
      await request(`/api/images/${encodeURIComponent(item.id)}`, 'DELETE');
      if (operationGeneration !== generation) return;
      notify('图片已删除');
      loadGallery();
    } catch (error) {
      handleError(error, operationGeneration);
    } finally {
      deleting.delete(item.id);
      button.disabled = false;
      button.textContent = '删除';
    }
  });
  $('closeCopyButton').addEventListener('click', () => $('copyDialog').close());
  $('copyDialog').addEventListener('close', () => { $('copyText').value = ''; });
  window.addEventListener('pagehide', () => disconnect('', false));

  $('uploadEndpoint').textContent = new URL('/api/upload', window.location.origin).href;
  updateConnection();
  readConfig();
})();
