import { createHuaweiPanel } from './huawei-panel.mjs';
import { copyText } from './clipboard.mjs';

// 总线与设备节点长期保留，轮询只更新反馈，切换总线也保留未提交的输入。
export function createRuntimeWorkspace(root, options = {}) {
  const document = root.ownerDocument;
  const fetch = options.fetch || globalThis.fetch;
  const buses = new Map();
  let selected = null;
  const element = (tag, className = '', text = '') => {
    const node = document.createElement(tag);
    node.className = className; node.textContent = text;
    return node;
  };
  const rail = element('aside', 'bus-rail');
  rail.setAttribute('aria-label', '运行总线');
  const railTitle = element('div', 'bus-rail-title', 'RS485 总线');
  const busList = element('div', 'bus-list');
  const content = element('div', 'bus-content');
  const empty = element('div', 'card runtime-empty', '正在读取运行状态…');
  rail.append(railTitle, busList); content.append(empty); root.append(rail, content);

  function select(key) {
    selected = key;
    for (const [busKey, entry] of buses) {
      entry.node.hidden = busKey !== key;
      entry.button.setAttribute('aria-pressed', String(busKey === key));
    }
    options.onSelect?.(buses.get(key)?.bus.name || '');
  }

  function makeBus(bus, key) {
    const entry = { bus, devices: new Map() };
    entry.button = element('button', 'bus-choice');
    entry.button.type = 'button'; entry.button.dataset.busKey = key;
    entry.button.setAttribute('aria-pressed', 'false');
    entry.label = element('strong'); entry.count = element('span');
    entry.button.append(entry.label, entry.count);
    entry.button.onclick = () => select(key);
    busList.append(entry.button);
    entry.node = element('section', 'card bus-workspace'); entry.node.dataset.busKey = key;
    const heading = element('div', 'bus-heading');
    const title = element('div'); title.append(element('span', 'section-eyebrow', '总线工作区'));
    entry.title = element('h2'); title.append(entry.title);
    entry.summary = element('span', 'bus-summary'); heading.append(title, entry.summary);
    const connection = element('div', 'bus-connection');
    entry.paths = [];
    const status = element('div', 'copy-path-status');
    status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    for (const [key, label] of [['slave_path', '实际设备'], ['link_path', '固定配置路径']]) {
      const row = element('div', 'bus-path-row');
      const value = element('code', 'bus-path-value');
      const button = element('button', 'copy-path', '复制'); button.type = 'button';
      button.setAttribute('aria-label', `复制${label}`);
      const item = { key, value, button, pending: false }; entry.paths.push(item);
      button.onclick = async () => {
        const path = entry.bus[key];
        if (!path || item.pending) return;
        item.pending = true; button.disabled = true; status.textContent = '正在复制…';
        try {
          await (options.copyText || copyText)(path, document);
          status.textContent = `${label}已复制`;
        } catch (error) {
          status.textContent = '复制失败，请手动选择路径复制。';
          console.error('复制串口路径失败', error);
        } finally { item.pending = false; button.disabled = !entry.bus[key]; }
      };
      row.append(element('span', 'bus-path-label', label), value, button); connection.append(row);
    }
    connection.append(element('div', 'bus-path-hint', '下位机 serial.device 请填写固定配置路径，重启后无需修改。实际设备编号由系统动态分配，重启后可能变化。'), status);
    entry.body = element('div', 'bus-devices');
    entry.node.append(heading, connection, entry.body); content.append(entry.node);
    return entry;
  }

  function makeDevice(bus, slave) {
    const entry = { rows: new Map() };
    entry.node = element('section', 'runtime-device'); entry.node.dataset.deviceId = String(slave.device_id);
    entry.staticTitle = element('h3', 'static-device-title', `Modbus 设备 · 站号 ${slave.device_id}`);
    entry.models = element('div', 'device-model');
    entry.panel = createHuaweiPanel(entry.models, { fetch, onApplied: options.onApplied });
    entry.registers = element('details', 'register-group runtime-registers');
    entry.registers.dataset.deviceId = String(slave.device_id);
    entry.summary = element('summary');
    entry.registers.append(entry.summary);
    entry.scroll = element('div', 'runtime-table-scroll');
    entry.message = element('p', 'register-message');
    entry.message.setAttribute('role', 'status'); entry.message.setAttribute('aria-live', 'polite');
    entry.registers.append(entry.scroll, entry.message);
    entry.node.append(entry.staticTitle, entry.models, entry.registers);
    return entry;
  }

  function makeTable(entry, bus, slave) {
    const table = element('table', 'runtime-table');
    const colgroup = element('colgroup');
    for (const cls of ['col-function', 'col-address', 'col-type', '', 'col-value', 'col-raw', 'col-manual']) colgroup.append(element('col', cls));
    const head = element('thead'); const headings = element('tr');
    for (const label of ['功能码', '地址', '类型', '点名', '当前值', '原始字节', '手动设置']) headings.append(element('th', '', label));
    head.append(headings); const body = element('tbody'); table.append(colgroup, head, body);
    entry.rows.clear();
    for (const reg of slave.registers || []) {
      const row = element('tr'); const cells = {};
      for (const [key, value] of Object.entries({ function: reg.function, address: reg.address, type: reg.type, tag: reg.tag || '', value: '', raw: '' })) {
        cells[key] = element('td', key === 'tag' ? 'tag' : 'num', String(value)); row.append(cells[key]);
      }
      const manual = element('td');
      const item = { cells, reg };
      if (reg.writable) {
        item.input = element('input'); item.input.type = 'text'; item.input.value = String(reg.value);
        item.input.setAttribute('aria-label', `站号 ${slave.device_id} 地址 ${reg.address} 手动值`);
        item.input.oninput = () => { item.dirty = true; };
        item.button = element('button', 'primary', '设置'); item.button.type = 'button';
        item.button.onclick = async () => {
          if (item.pending) return;
          const raw = item.input.value;
          const value = Number(raw);
          if (!raw.trim() || !Number.isFinite(value)) { entry.message.textContent = '请输入有效数字'; return; }
          item.pending = true; item.button.disabled = true;
          entry.message.textContent = '正在设置…'; entry.message.className = 'register-message';
          try {
            const response = await fetch('/api/manual', {
              method: 'POST', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ bus: entry.bus, device_id: slave.device_id, function: reg.function, address: reg.address, value }),
            });
            const result = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error(result.error || `请求失败（${response.status}）`);
            // 请求期间继续输入的新草稿不属于这次提交，成功响应不能清除它。
            if (item.input.value === raw) item.dirty = false;
            entry.message.textContent = '已设置，等待反馈刷新。'; options.onApplied?.();
          } catch (error) {
            entry.message.className = 'register-message error'; entry.message.textContent = `设置失败：${error.message}`;
            console.error('设置寄存器失败', error);
          } finally { item.pending = false; item.button.disabled = false; }
        };
        manual.append(item.input, ' ', item.button);
      } else {
        manual.append(element('span', 'tag', reg.model_managed ? '模型派生 · 只读' : (slave.model ? '只读 · 未实现或静态点' : '只读')));
      }
      row.append(manual); body.append(row); entry.rows.set(JSON.stringify([reg.function, reg.address]), item);
    }
    entry.scroll.replaceChildren(table);
  }

  function update(state) {
    const next = state.buses || [];
    const keys = new Set(next.map(bus => bus.link_path || bus.name));
    for (const [key, entry] of buses) if (!keys.has(key)) { entry.node.remove(); entry.button.remove(); buses.delete(key); }
    empty.hidden = next.length > 0;
    empty.textContent = '还没有运行总线，请进入 RS485 配置添加总线和设备。';
    railTitle.textContent = `RS485 总线 · ${next.length}`;
    for (const bus of next) {
      const key = bus.link_path || bus.name;
      let entry = buses.get(key);
      if (!entry) { entry = makeBus(bus, key); buses.set(key, entry); }
      entry.bus = bus; entry.label.textContent = bus.name; entry.title.textContent = bus.name;
      const slaves = bus.slaves || [];
      const total = slaves.reduce((sum, slave) => sum + (slave.registers || []).length, 0);
      entry.count.textContent = `${slaves.length} 个设备 · ${total} 个寄存器`;
      entry.summary.textContent = `${slaves.length} 个设备 / ${total} 个寄存器`;
      for (const item of entry.paths) {
        item.value.textContent = bus[item.key] || '未就绪';
        item.button.disabled = item.pending || !bus[item.key];
      }
      const ids = new Set(slaves.map(slave => slave.device_id));
      for (const [id, device] of entry.devices) if (!ids.has(id)) { device.node.remove(); entry.devices.delete(id); }
      for (const slave of slaves) {
        let device = entry.devices.get(slave.device_id);
        if (!device) { device = makeDevice(bus, slave); entry.devices.set(slave.device_id, device); entry.body.append(device.node); }
        device.bus = bus.name;
        device.staticTitle.hidden = slave.model?.kind === 'huawei_v3';
        device.panel.update({ now: state.now, buses: [{ ...bus, slaves: [slave] }] });
        device.summary.textContent = `寄存器与手动设值 · 站号 ${slave.device_id} · ${(slave.registers || []).length} 项`;
        const signature = JSON.stringify((slave.registers || []).map(reg => [reg.function, reg.address, reg.type, reg.writable, reg.model_managed]));
        if (device.signature !== signature) { makeTable(device, bus, slave); device.signature = signature; }
        for (const reg of slave.registers || []) {
          const item = device.rows.get(JSON.stringify([reg.function, reg.address]));
          item.cells.value.textContent = reg.unit ? `${reg.value} ${reg.unit}` : String(reg.value);
          item.cells.raw.textContent = reg.raw || '';
          item.cells.tag.textContent = reg.tag || '';
          for (const cell of Object.values(item.cells)) cell.title = cell.textContent;
          if (item.input && document.activeElement !== item.input && !item.pending && !item.dirty) item.input.value = String(reg.value);
        }
      }
      entry.node.classList?.toggle('bus-is-empty', !slaves.length);
    }
    if (!buses.has(selected)) selected = next.length ? next[0].link_path || next[0].name : null;
    select(selected);
  }

  function reset() {
    for (const entry of buses.values()) { entry.node.remove(); entry.button.remove(); }
    buses.clear(); empty.hidden = false; empty.textContent = '正在刷新已应用的配置…';
  }
  function setError() {
    if (!buses.size) { empty.hidden = false; empty.textContent = '读取运行状态失败，正在重试连接…'; }
    for (const entry of buses.values()) {
      entry.summary.textContent = '连接中断 · 反馈可能过期';
      for (const device of entry.devices.values()) device.panel.setError();
    }
  }
  return { update, reset, setError };
}

if (typeof document !== 'undefined') {
  const root = document.getElementById('buses');
  if (root) {
    const workspace = createRuntimeWorkspace(root, { fetch: window.fetch.bind(window), onApplied: () => window.dispatchEvent(new Event('model-applied')) });
    window.addEventListener('sim-state', event => workspace.update(event.detail));
    window.addEventListener('sim-state-error', () => workspace.setError());
    window.addEventListener('config-applied', () => workspace.reset());
    window.dispatchEvent(new Event('model-applied'));
  }
}
