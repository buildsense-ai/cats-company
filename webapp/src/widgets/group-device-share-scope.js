const operationNames = {
  read_file: '读取文件', resolve_common_directory: '定位目录', glob: '查找文件', grep: '搜索内容',
  write_file: '写入文件', edit_file: '修改文件', send_file: '发送文件', execute_shell: '执行命令',
  'skillhub.localWorkspace.get': '读取技能工作区', 'skillhub.localWorkspace.syncToAgent': '同步技能到助手',
  'skillhub.localSkill.share': '共享技能', 'skillhub.localSkill.finalize': '保存技能',
  'skillhub.localSkill.delete': '删除技能', 'skillhub.localBot.switch': '切换设备助手',
  'knowledge.document.list': '列出知识文档', 'knowledge.document.read': '读取知识文档',
};

export const knownShareScope = operations => Array.isArray(operations)
  && operations.every(operation => Object.hasOwn(operationNames, operation));

export function describeShareScope(operations) {
  if (!knownShareScope(operations)) return '授权范围暂不可确认，请刷新';
  return [...new Set(operations)].map(operation => operationNames[operation]).join('、') || '无可用操作';
}
