import { modelDevices, getDeviceView, controlFields, scenarioFields, controlRequest, scenarioRequest, sendHuaweiRequest, appendPowerSample, powerTrendView } from './huawei-view.mjs';

export function createHuaweiPanel(root, options = {}) {
  const document = root.ownerDocument;
  const fetch = options.fetch || globalThis.fetch;
  const cards = new Map();
  function element(tag, className = '', text = '') {
    const node = document.createElement(tag);
    node.className = className; node.textContent = text;
    return node;
  }
  function button(label, handler, primary = false) {
    const node = element('button', primary ? 'primary' : '', label);
    node.type = 'button'; node.onclick = handler;
    return node;
  }
  function makeCard(device) {
    const card = { device, dirtyScenario: false, pending: false, inputs: {}, history: [] };
    card.node = element('section', 'card huawei-card');
    const heading = element('div', 'huawei-heading');
    const identity = element('div', 'device-identity');
    identity.append(element('span', 'section-eyebrow', `站号 ${device.device_id} · 行为模型`), element('h3', '', '华为逆变器 · Huawei V3'));
    heading.append(identity);
    card.status = element('span', 'huawei-status'); heading.append(card.status);
    card.node.append(heading);
    const body = element('div', 'huawei-body');
    const feedback = element('div', 'huawei-feedback');
    const metrics = element('div', 'huawei-metrics');
    card.metrics = {};
    for (const [key, label] of Object.entries({ active: '实际有功', targetActive: '目标有功', reactive: '实际无功', targetReactive: '目标无功', powerFactor: '功率因数', dayEnergy: '当日电量', totalEnergy: '累计电量' })) {
      const metric = element('div', 'huawei-metric'); const value = element('strong', '', '—');
      metric.append(element('span', '', label), value); card.metrics[key] = value; metrics.append(metric);
    }
    card.commands = element('p', 'huawei-caption');
    const trend = element('div', 'huawei-trend');
    const trendLegend = element('div', 'huawei-trend-legend');
    trendLegend.append(element('strong', '', '有功趋势'), element('span', 'huawei-trend-actual', '实际'), element('span', 'huawei-trend-target', '目标'));
    const svg = tag => document.createElementNS('http://www.w3.org/2000/svg', tag);
    card.chart = svg('svg'); card.chart.setAttribute('viewBox', '0 -10 320 140');
    card.chart.setAttribute('preserveAspectRatio', 'none');
    card.chart.setAttribute('role', 'img'); card.chart.setAttribute('aria-label', '有功趋势，蓝色实线为实际功率，橙色虚线为目标功率');
    card.chart.setAttribute('class', 'huawei-trend-chart');
    for (const y of [0, 60, 120]) {
      const grid = svg('line'); grid.setAttribute('x1', '0'); grid.setAttribute('x2', '320'); grid.setAttribute('y1', String(y)); grid.setAttribute('y2', String(y)); grid.setAttribute('class', 'huawei-trend-grid'); card.chart.append(grid);
    }
    card.paths = {};
    for (const [key, series] of [['target', 'target'], ['active', 'actual']]) {
      const path = svg('polyline'); path.dataset.series = series; path.setAttribute('class', `huawei-trend-${series}`); path.setAttribute('fill', 'none');
      path.setAttribute('vector-effect', 'non-scaling-stroke'); card.paths[key] = path; card.chart.append(path);
    }
    card.trendCaption = element('p', 'huawei-caption', '等待后续反馈采样…');
    trend.append(trendLegend, card.chart, card.trendCaption);
    const table = element('table', 'huawei-phases');
    const header = element('tr');
    for (const name of ['相 / 线', '线电压', '相电压', '相电流']) header.append(element('th', '', name));
    const thead = element('thead'); thead.append(header); table.append(thead);
    const tbody = element('tbody'); card.phases = [];
    for (let index = 0; index < 3; index++) {
      const row = element('tr'); const cells = {};
      for (const key of ['name', 'lineVoltage', 'phaseVoltage', 'current']) { cells[key] = element('td', 'num', '—'); row.append(cells[key]); }
      card.phases.push(cells); tbody.append(row);
    }
    table.append(tbody);
    card.node.append(metrics);
    feedback.append(trend, card.commands, table); body.append(feedback);

    const controls = element('div', 'huawei-controls');
    const controlForm = element('form'); controlForm.noValidate = true;
    const controlSet = element('fieldset', 'huawei-fieldset');
    controlSet.append(element('legend', '', '控制命令'));
    const presets = element('div', 'huawei-presets');
    const controlMessage = element('p', 'huawei-message', '写入命令后，请观察目标与实际功率按梯度变化。恢复100%仅恢复有功限值。');
    controlMessage.setAttribute('role', 'status'); controlMessage.setAttribute('aria-live', 'polite');
    const scenarioMessage = element('p', 'huawei-message', '场景变更只影响当前运行；保存配置后会恢复配置初值。');
    scenarioMessage.setAttribute('role', 'status'); scenarioMessage.setAttribute('aria-live', 'polite');
    async function submit(endpoint, payloadFactory, message, successful) {
      if (card.pending) return;
      try {
        const payload = payloadFactory();
        card.pending = true; controlSet.disabled = true; scenarioSet.disabled = true;
        message.className = 'huawei-message'; message.textContent = '正在提交…';
        await sendHuaweiRequest(fetch, endpoint, payload);
        if (successful) successful();
        message.textContent = '已应用；目标与实际反馈将在刷新后更新。';
        options.onApplied?.();
      } catch (error) {
        message.className = 'huawei-message error'; message.textContent = `应用失败：${error.message}`;
      } finally {
        card.pending = false; controlSet.disabled = false; scenarioSet.disabled = false;
      }
    }
    const control = (kind, value) => submit('/api/manual', () => controlRequest(card.device, kind, value), controlMessage);
    for (const [label, kind, value] of [['50% 限功率', 'percent', 50], ['10 kW', 'fixed', 10], ['PF 0.9', 'pf', 0.9], ['恢复 100% 有功', 'percent', 100]]) presets.append(button(label, () => control(kind, value)));
    controlSet.append(presets);
    for (const field of controlFields) {
      const row = element('div', 'huawei-command'); const label = element('label', '', field.label);
      const input = element('input'); input.type = 'number'; input.name = field.key;
      input.min = field.min; input.max = field.max; input.step = field.step; input.value = String(field.initial);
      const write = button('写入', () => control(field.key, input.value), true);
      write.setAttribute('aria-label', `写入${field.label}`);
      label.append(input); row.append(label, write); controlSet.append(row);
    }
    controlForm.onsubmit = event => event.preventDefault();
    controlForm.append(controlSet, controlMessage); controls.append(controlForm);

    const scenarioForm = element('form'); scenarioForm.noValidate = true; scenarioForm.dataset.form = 'scenario';
    const scenarioSet = element('fieldset', 'huawei-fieldset'); scenarioSet.append(element('legend', '', '光照与电网场景'));
    const solar = element('label', 'huawei-solar', '☀ 太阳可用直流功率');
    card.solarValue = element('strong', '', '—');
    const slider = element('input'); slider.type = 'range'; slider.min = 0; slider.max = 100; slider.step = 0.1;
    slider.setAttribute('aria-label', '太阳可用直流功率滑块（kW）');
    const solarLabel = value => String(value).trim() && Number.isFinite(Number(value)) ? `${Number(value).toFixed(2)} kW` : '—';
    slider.oninput = () => { card.dirtyScenario = true; card.inputs.dc_available_kw.value = slider.value; card.solarValue.textContent = solarLabel(slider.value); };
    card.slider = slider; solar.append(card.solarValue, slider); scenarioSet.append(solar);
    const scenarioGrid = element('div', 'config-grid');
    for (const field of scenarioFields) {
      const label = element('label', 'config-field'); label.append(element('span', '', field.label));
      const input = element('input'); input.type = 'number'; input.name = field.key; input.min = field.min; input.max = field.max; input.step = field.step;
      input.value = String(device.model.scenario?.[field.key] ?? ''); input.required = true;
      input.oninput = () => {
        card.dirtyScenario = true;
        if (field.key === 'dc_available_kw') {
          card.solarValue.textContent = solarLabel(input.value);
          if (input.value && Number.isFinite(Number(input.value))) slider.value = input.value;
        }
      };
      card.inputs[field.key] = input; label.append(input); scenarioGrid.append(label);
    }
    const apply = element('button', 'primary', '应用场景'); apply.type = 'submit';
    scenarioSet.append(scenarioGrid, apply);
    scenarioForm.onsubmit = event => {
      event.preventDefault();
      return submit('/api/scenario', () => scenarioRequest(card.device, Object.fromEntries(Object.entries(card.inputs).map(([key, input]) => [key, input.value]))), scenarioMessage, () => { card.dirtyScenario = false; });
    };
    scenarioForm.append(scenarioSet, scenarioMessage); controls.append(scenarioForm);
    body.append(controls); card.node.append(body);
    const explanation = element('details', 'huawei-explanation');
    explanation.append(element('summary', '', '查看仿真范围与控制规则说明'), element('p', '', '有功百分比与固定值、无功PF与Q/S分别采用最后一次成功写入的命令。百分比参照旧版华为协议以Pmax为基准；有功/无功梯度以Pn/Smax为基准。控制优先级、PF正负符号、非零反馈模式及状态枚举的版本迁移属于仿真假设。无功受Smax及馈入/吸收限值限制，采用有功优先。电压、频率变更更新电参，不模拟厂家保护、重启或电网潮流。身份、未建模告警、绝缘及时间等点保留模板值。'));
    explanation.append(element('p', '', '当前版本按现有点表模拟4路PV、平衡三相。未实现参数只读；保存并应用配置会重建模型，重置临时命令及运行电量到模板初值。'));
    card.node.append(explanation);
    return card;
  }
  function update(state) {
    const devices = modelDevices(state);
    const keys = new Set(devices.map(device => device.key));
    for (const [key, card] of cards) if (!keys.has(key)) { card.node.remove(); cards.delete(key); }
    root.hidden = devices.length === 0;
    for (const device of devices) {
      let card = cards.get(device.key);
      if (!card) { card = makeCard(device); cards.set(device.key, card); root.append(card.node); }
      card.device = device;
      const view = getDeviceView(device);
      card.status.textContent = view.status;
      card.status.dataset.state = view.status;
      for (const [key, value] of Object.entries(card.metrics)) value.textContent = view[key];
      card.commands.textContent = `当前有功命令：${view.activeCommand}　当前无功命令：${view.reactiveCommand}`;
      view.phases.forEach((phase, index) => { for (const [key, value] of Object.entries(phase)) card.phases[index][key].textContent = value; });
      card.history = appendPowerSample(card.history, state, device);
      const trend = powerTrendView(card.history);
      for (const [key, path] of Object.entries(card.paths)) path.setAttribute('points', trend[key]);
      card.trendCaption.textContent = trend.caption;
      if (card.history.length < 2) card.chart.setAttribute('hidden', '');
      else card.chart.removeAttribute('hidden');
      // 控制卡不重建，草稿与反馈分开更新，轮询期间不会夺走焦点。
      if (!card.dirtyScenario && !card.pending) {
        for (const field of scenarioFields) card.inputs[field.key].value = String(device.model.scenario?.[field.key] ?? '');
        card.slider.value = card.inputs.dc_available_kw.value;
        card.solarValue.textContent = `${Number(card.inputs.dc_available_kw.value).toFixed(2)} kW`;
      }
    }
  }
  function reset() {
    cards.clear(); root.replaceChildren(); root.hidden = true;
  }
  function setError() {
    for (const card of cards.values()) {
      card.status.textContent = '连接中断 · 反馈可能过期';
      card.status.dataset.state = '连接中断';
    }
  }
  return { update, reset, setError };
}

if (typeof document !== 'undefined') {
  const root = document.getElementById('huawei-models');
  if (root) {
    const panel = createHuaweiPanel(root, { fetch: window.fetch.bind(window), onApplied: () => window.dispatchEvent(new Event('model-applied')) });
    window.addEventListener('sim-state', event => panel.update(event.detail));
    window.addEventListener('sim-state-error', () => panel.setError());
    window.addEventListener('config-applied', () => panel.reset());
    // 模块加载也请求一次状态，避免首个轮询早于模块初始化时遗漏。
    window.dispatchEvent(new Event('model-applied'));
  }
}
