import { newSlave, normalizeConfig, validateDraft } from './config-model.mjs';
import { scenarioRequest } from './huawei-view.mjs';

export const customDeviceType = '__custom_modbus__';

// 站号仅在当前总线内分配，不改动既有设备。
export function nextDeviceID(slaves) {
  const used = new Set(slaves.map(slave => Number(slave.device_id)));
  for (let id = 1; id <= 247; id++) if (!used.has(id)) return id;
  throw new Error('当前总线站号1～247已用满，请删除设备或新增RS485总线');
}

function normalizedTemplate(template) {
  if (!template || typeof template.id !== 'string' || !template.id.trim() || template.id === customDeviceType || typeof template.name !== 'string' || !template.name.trim() || !Array.isArray(template.slave?.registers) || !template.slave.registers.length) throw new Error('设备预置格式错误，必须包含名称、唯一标识及完整寄存器点表');
  let config;
  try {
    config = normalizeConfig({ buses: [{ name: '设备预置', link: '/tmp/device-template', slaves: [template.slave] }] });
  } catch { throw new Error('设备预置格式错误，无法读取寄存器参数'); }
  const error = validateDraft(config);
  if (error) throw new Error(`设备预置无效：${error}`);
  const slave = config.buses[0].slaves[0];
  if (slave.model?.kind === 'huawei_v3') scenarioRequest({}, slave.model);
  return { id: template.id, name: template.name, slave };
}

export function parseDeviceTemplates(value) {
  if (!Array.isArray(value)) throw new Error('设备预置响应格式错误，必须是设备目录列表');
  const ids = new Set();
  return value.map(template => {
    const item = normalizedTemplate(template);
    if (ids.has(item.id)) throw new Error('设备预置响应格式错误，设备标识重复');
    ids.add(item.id);
    return item;
  });
}

// 返回独立草稿对象，调用方明确加入目标总线后才成为未保存配置。
export function createDeviceSlave(slaves, template = null) {
  const device_id = nextDeviceID(slaves);
  const slave = template ? normalizedTemplate(template).slave : newSlave(slaves);
  slave.device_id = device_id;
  return slave;
}

export async function loadDeviceTemplates(fetch) {
  let response;
  try { response = await fetch('/api/device-templates', { method: 'GET', cache: 'no-store' }); }
  catch { throw new Error('设备预置连接失败，请重试'); }
  const value = await response.json().catch(() => null);
  if (!response.ok) throw new Error(value?.error || `设备预置读取失败（${response.status}）`);
  return parseDeviceTemplates(value);
}
