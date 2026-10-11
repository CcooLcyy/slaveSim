import { newBus, newRegister, normalizeConfig, validateDraft, sourceFields } from './config-model.mjs';
import { scenarioFields, scenarioRequest } from './huawei-view.mjs';
import { customDeviceType, createDeviceSlave, loadDeviceTemplates } from './device-templates.mjs';

const root = document.getElementById('config-buses');
const status = document.getElementById('config-status');
const form = document.getElementById('config-form');
let draft = null;
let dirty = false;
let busy = false;
const expandedRegisters = new WeakMap();
const registerGroups = new WeakMap();
const selectedDeviceTypes = new WeakMap();
let deviceTemplates = [];
let templatesLoading = false;
let templatesLoaded = false;
let templatesError = '';
let deviceControls = [];

function updateDeviceButton(control) {
  const type = control.select.value;
  control.button.disabled = type !== customDeviceType && !deviceTemplates.some(template => template.id === type);
}

function fillDeviceChoices(control) {
  const selected = selectedDeviceTypes.get(control.bus) || customDeviceType;
  control.select.replaceChildren();
  const custom = element('option', '', '自定义 Modbus 设备'); custom.value = customDeviceType; control.select.append(custom);
  for (const template of deviceTemplates) {
    const option = element('option', '', template.name); option.value = template.id; control.select.append(option);
  }
  if (!deviceTemplates.length) {
    const hint = element('option', '', templatesLoading ? '正在加载预置设备…' : (templatesError ? '预置设备加载失败' : '暂无预置设备'));
    hint.disabled = true; control.select.append(hint);
  }
  control.select.value = selected === customDeviceType || deviceTemplates.some(template => template.id === selected) ? selected : customDeviceType;
  selectedDeviceTypes.set(control.bus, control.select.value);
  updateDeviceButton(control);
}

function showTemplateStatus() {
  const node = document.getElementById('device-template-status');
  node.className = templatesError ? 'config-status error' : 'config-status';
  node.textContent = templatesLoading ? '正在读取预置设备；自定义 Modbus 设备仍可添加。' : templatesError
    ? `预置设备加载失败：${templatesError}。仍可读取、编辑和保存原配置，也可添加自定义 Modbus 设备。`
    : `已读取 ${deviceTemplates.length} 种预置设备；选择设备类型并添加到草稿，保存并应用后才生效。`;
  const retry = document.getElementById('retry-device-templates'); retry.hidden = !templatesError; retry.disabled = templatesLoading;
  // 异步目录读取只更新新增设备选项，不重绘配置输入或替换草稿。
  for (const control of deviceControls) fillDeviceChoices(control);
}

async function readDeviceTemplates() {
  if (templatesLoading) return;
  templatesLoading = true; templatesError = ''; showTemplateStatus();
  try { deviceTemplates = await loadDeviceTemplates(window.fetch ? window.fetch.bind(window) : fetch); templatesLoaded = true; }
  catch (error) { deviceTemplates = []; templatesError = error.message; }
  finally { templatesLoading = false; showTemplateStatus(); }
}

function deviceAdder(bus) {
  const area = element('div', 'device-add');
  const label = element('label', 'config-field'); label.append(element('span', '', '设备类型'));
  const select = element('select'); select.name = 'device-type'; select.setAttribute('aria-label', '设备类型'); label.append(select);
  const button = element('button', 'primary', '添加设备'); button.type = 'button';
  const control = { bus, select, button }; deviceControls.push(control); fillDeviceChoices(control);
  select.onchange = () => { selectedDeviceTypes.set(bus, select.value); updateDeviceButton(control); };
  button.onclick = () => {
    if (busy) return;
    if (!draft?.buses.includes(bus)) { message('目标总线已移除，请在当前总线中添加设备', true); return; }
    try {
      const type = select.value;
      const template = type === customDeviceType ? null : deviceTemplates.find(template => template.id === type);
      if (type !== customDeviceType && !template) throw new Error('所选预置设备不可用，请重试加载或选择自定义 Modbus 设备');
      const slave = createDeviceSlave(bus.slaves, template);
      bus.slaves.push(slave); changed(); renderEditor();
      message(`已将${template?.name || '自定义 Modbus 设备'}加入总线 ${bus.name} 的草稿（站号 ${slave.device_id}）；保存并应用后生效`);
      console.info('设备已加入配置草稿', { bus: bus.name, device_id: slave.device_id, type });
    } catch (error) { message(`添加设备失败：${error.message}`, true); }
  };
  area.append(label, button);
  return area;
}

function message(text, error = false) {
  status.textContent = text;
  status.className = error ? 'config-status error' : 'config-status';
}

function changed() {
  dirty = true;
  message('有未保存的修改');
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function action(label, handler, danger = false) {
  const button = element('button', danger ? 'danger' : '', label);
  button.type = 'button';
  button.onclick = () => {
    if (busy) return;
    handler(); changed(); renderEditor();
  };
  return button;
}

function field(parent, object, key, label, options = {}) {
  const wrapper = element('label', 'config-field');
  wrapper.append(element('span', '', label));
  let input;
  if (options.choices) {
    input = element('select');
    for (const [value, text] of options.choices) {
      const option = element('option', '', text);
      option.value = value; input.append(option);
    }
  } else {
    input = element('input');
    input.type = options.numeric ? 'number' : 'text';
    if (options.numeric) input.step = options.integer ? '1' : 'any';
    if (options.min !== undefined) input.min = options.min;
    if (options.max !== undefined) input.max = options.max;
    if (options.placeholder) input.placeholder = options.placeholder;
  }
  input.value = object[key] ?? '';
  input.required = Boolean(options.required);
  input.oninput = () => {
    object[key] = options.numeric ? (input.value === '' ? null : Number(input.value)) : input.value;
    changed();
  };
  input.onchange = () => {
    if (options.afterChange) options.afterChange();
  };
  wrapper.append(input);
  parent.append(wrapper);
  return input;
}

const choices = items => items.map(item => Array.isArray(item) ? item : [item, item]);
const sourceLabels = { const: '固定值', manual: '手动初值', sine: '正弦', ramp: '斜坡', random: '随机' };
const parameterLabels = { value: '原始值', base: '基值', amplitude: '振幅', period_ms: '周期 / 更新间隔（毫秒）', min: '最小值', max: '最大值' };

function modelEditor(parent, slave) {
  if (!slave.model) return;
  const section = element('section', 'model-editor');
  section.append(element('h5', '', '华为逆变器仿真 · 模型初始参数'));
  if (slave.model.kind === 'huawei_v3') {
    const fields = element('div', 'config-grid');
    for (const parameter of scenarioFields) {
      field(fields, slave.model, parameter.key, parameter.label, { numeric: true, min: parameter.min, max: parameter.max, required: true });
    }
    section.append(fields, element('p', '', '这些参数随配置保存，运行页“应用场景”只临时修改当前运行。保存并应用配置会重建模型、重置临时命令及运行电量到模板初值；无模型的旧从站保持原数值来源规则。'));
  } else {
    section.append(element('p', '', `当前页面不支持模型 ${slave.model.kind || '未指定类型'} 的参数编辑`));
  }
  parent.append(section);
}

function registerEditor(parent, slave, reg, index) {
  const details = element('details', 'register-editor');
  details.open = expandedRegisters.get(reg) ?? slave.registers.length <= 3;
  details.ontoggle = () => expandedRegisters.set(reg, details.open);
  const summary = element('summary', '', `寄存器 ${index + 1} · 地址 ${reg.address}${reg.tag ? ` · ${reg.tag}` : ''}`);
  details.append(summary);
  const fields = element('div', 'config-grid');
  const updateSummary = () => { summary.textContent = `寄存器 ${index + 1} · 地址 ${reg.address}${reg.tag ? ` · ${reg.tag}` : ''}`; };
  field(fields, reg, 'function', '功能码', { choices: choices(['0x01', '0x03', '0x04', '0x05', '0x06', '0x10']) });
  field(fields, reg, 'address', '协议地址', { numeric: true, integer: true, min: 0, max: 65535, required: true, afterChange: updateSummary });
  field(fields, reg, 'type', '数据类型', { choices: choices(['UINT16', 'INT16', 'UINT32', 'INT32', 'BOOL']) });
  field(fields, reg, 'tag', '点名', { afterChange: updateSummary });
  field(fields, reg, 'unit', '显示单位');
  field(fields, reg, 'access', '访问权限', { choices: choices([['readonly', '只读'], ['readwrite', '读写']]) });
  field(fields, reg, 'word_order', '32 位字序', { choices: choices([['HL', 'HL · 高字在前'], ['LH', 'LH · 低字在前']]) });
  field(fields, reg, 'byte_order', '字节序', { choices: choices([['AB', 'AB · 高字节在前'], ['BA', 'BA · 低字节在前']]) });
  field(fields, reg, 'bit_index', 'BOOL 位号', { numeric: true, integer: true, min: 0, max: 15, required: true });
  const sourceParameters = element('div', 'config-grid source-parameters');
  const renderSource = () => {
    sourceParameters.replaceChildren();
    for (const key of sourceFields(reg.source.kind)) {
      field(sourceParameters, reg.source, key, parameterLabels[key], { numeric: true, integer: key === 'period_ms', min: key === 'period_ms' ? 1 : undefined, required: true });
    }
  };
  field(fields, reg.source, 'kind', '数值来源', { choices: Object.entries(sourceLabels), afterChange: renderSource });
  renderSource();
  details.append(fields, sourceParameters, action('删除寄存器', () => slave.registers.splice(index, 1), true));
  parent.append(details);
}

function renderEditor() {
  root.replaceChildren();
  deviceControls = [];
  if (!draft) return;
  document.getElementById('config-count').textContent = `${draft.buses.length} 路总线 · ${draft.buses.reduce((n, bus) => n + bus.slaves.length, 0)} 个从站`;
  for (const [busIndex, bus] of draft.buses.entries()) {
    const card = element('section', 'bus-editor');
    const heading = element('div', 'editor-heading');
    const title = element('h3', '', bus.name || `RS485 总线 ${busIndex + 1}`);
    heading.append(title, action('删除总线', () => draft.buses.splice(busIndex, 1), true));
    const fields = element('div', 'config-grid bus-fields');
    field(fields, bus, 'name', '总线名称', { required: true, afterChange: () => { title.textContent = bus.name || '未命名总线'; } });
    field(fields, bus, 'link', '串口软链接路径', { required: true, placeholder: '/srv/serial-sim/tty0' });
    card.append(heading, fields, deviceAdder(bus));
    for (const [slaveIndex, slave] of bus.slaves.entries()) {
      const station = element('section', 'slave-editor');
      const stationHeading = element('div', 'editor-heading');
      const stationTitle = element('h4', '', `从站 ${slave.device_id}`);
      stationHeading.append(stationTitle, action('删除从站', () => bus.slaves.splice(slaveIndex, 1), true));
      const stationFields = element('div', 'config-grid');
      field(stationFields, slave, 'device_id', '站号（1～247）', { numeric: true, integer: true, min: 1, max: 247, required: true, afterChange: () => { stationTitle.textContent = `从站 ${slave.device_id}`; } });
      field(stationFields, slave, 'response_delay_ms', '响应延迟（毫秒）', { numeric: true, integer: true, min: 0, max: 60000, required: true });
      field(stationFields, slave, 'unmapped_policy', '未配置地址', { choices: choices([['exception', '返回异常码 0x02'], ['zero', '返回 0']]) });
      station.append(stationHeading, stationFields);
      modelEditor(station, slave);
      const registers = element('details', 'register-group');
      // 直接读取上次节点，避免异步 toggle 事件在重绘时丢失展开状态。
      registers.open = registerGroups.get(slave)?.open ?? false;
      registerGroups.set(slave, registers);
      registers.append(element('summary', '', `寄存器列表 · ${slave.registers.length} 项`));
      for (const [index, reg] of slave.registers.entries()) registerEditor(registers, slave, reg, index);
      station.append(registers);
      station.append(action('新增寄存器', () => {
        const reg = newRegister(slave.registers);
        registers.open = true;
        expandedRegisters.set(reg, true);
        slave.registers.push(reg);
      }));
      card.append(station);
    }
    root.append(card);
  }
  if (!draft.buses.length) root.append(element('p', 'config-empty', '还没有 RS485 总线，请点击“新增 RS485 总线”。'));
}

function setBusy(value) {
  busy = value;
  document.getElementById('config-fields').disabled = value;
  document.getElementById('save-config').textContent = value ? '处理中…' : '保存并应用';
}

async function readResponse(response) {
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `请求失败（${response.status}）`);
  return body;
}

async function loadConfig() {
  if (busy) return;
  if (dirty && !window.confirm('重新读取会丢弃尚未保存的配置修改，是否继续？')) return;
  setBusy(true);
  try {
    draft = normalizeConfig(await readResponse(await fetch('/api/config', { cache: 'no-store' })));
    dirty = false; renderEditor(); message('已读取当前配置');
  } catch (error) {
    message(`读取失败：${error.message}`, true);
  } finally { setBusy(false); }
}

form.onsubmit = async event => {
  event.preventDefault();
  if (busy || !draft) return;
  const error = validateDraft(draft);
  if (error) { message(error, true); return; }
  for (const bus of draft.buses) for (const slave of bus.slaves) {
    if (slave.model?.kind !== 'huawei_v3') continue;
    try { scenarioRequest({ bus: bus.name, device_id: slave.device_id }, slave.model); }
    catch (error) { message(`总线 ${bus.name} / 站号 ${slave.device_id}：${error.message}`, true); return; }
  }
  setBusy(true); message('正在保存并应用配置…');
  try {
    await readResponse(await fetch('/api/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(draft) }));
    dirty = false;
    message('配置已保存并应用；临时手动值已重置，行为模型按配置初值重建');
    console.info('多路 RS485 配置已保存并应用', { buses: draft.buses.length });
    window.dispatchEvent(new Event('config-applied'));
  } catch (error) {
    message(`保存失败，草稿已保留：${error.message}`, true);
    console.error('保存多路 RS485 配置失败', error);
  } finally { setBusy(false); }
};

document.getElementById('add-bus').onclick = () => {
  if (!draft || busy) return;
  draft.buses.push(newBus(draft.buses)); changed(); renderEditor();
};
document.getElementById('reload-config').onclick = loadConfig;
document.getElementById('retry-device-templates').onclick = readDeviceTemplates;
document.getElementById('show-config').onclick = () => {
  if (!draft) loadConfig();
  if (!templatesLoaded && !templatesLoading) readDeviceTemplates();
};
window.addEventListener('beforeunload', event => {
  if (!dirty) return;
  event.preventDefault(); event.returnValue = '';
});
