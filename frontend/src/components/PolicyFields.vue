<script setup>
// 策略表单字段（唯一实现）。
//
// 策略可以在两个地方创建：策略与监控页、主机清单（按主机创建/主机扫描后生成）。
// 两处的可选配置必须始终一致，所以字段只在这里定义一次，由两个页面共用；
// 任何新增字段都只需改这一个文件。
//
// form 是一个普通对象（由父组件用 reactive 定义），本组件直接读写它的属性，
// 与 v-model 的用法等价。
import Switch from './Switch.vue'
import TagInput from './TagInput.vue'

const props = defineProps({
  form: { type: Object, required: true },
  // 主机入口：匹配范围固定为 mac 且宿主机已知，此时不再让人手填 MAC 列表。
  hostBound: { type: Boolean, default: false },
})

const KINDS = [
  { value: 'any', label: '任意' },
  { value: 'local', label: '本机' },
  { value: 'cidr', label: '网段 / 局域网' },
  { value: 'remote', label: '远端' },
  { value: 'mac', label: 'MAC 地址 / 前缀' },
]

const ACTIONS = [
  { value: 'watch', label: '仅观察' },
  { value: 'add', label: '自动添加' },
  { value: 'ignore', label: '忽略' },
  { value: 'delete', label: '删除记录' },
]

const isKnownKind = (k) => KINDS.some((x) => x.value === k)
</script>

<template>
  <div class="grid cols-2">
    <label class="field">
      <span>名称 *</span>
      <input v-model="form.name" class="input" placeholder="如 家庭服务白名单" />
    </label>
    <label class="field">
      <span>优先级（越大越先匹配）</span>
      <input v-model.number="form.priority" class="input" type="number" />
    </label>
  </div>

  <label class="field">
    <span>说明</span>
    <input v-model="form.description" class="input" />
  </label>

  <div class="grid cols-2">
    <label class="field">
      <span>匹配范围</span>
      <select v-model="form.match_target_kind" class="select">
        <option v-for="k in KINDS" :key="k.value" :value="k.value">{{ k.label }}</option>
        <!-- 历史或未知类型：显示原值，避免下拉渲染成空白 -->
        <option v-if="!isKnownKind(form.match_target_kind)" :value="form.match_target_kind">
          {{ form.match_target_kind || '（未设置）' }}
        </option>
      </select>
    </label>
    <label v-if="form.match_target_kind === 'mac'" class="field">
      <span>MAC 地址 / 前缀</span>
      <input
        v-model="form.mac_patterns"
        class="input"
        :placeholder="hostBound ? '由主机自动填入' : 'aa:bb:cc:dd:ee:ff, aa:bb:cc:* （逗号分隔，可用 OUI 前缀）'"
      />
      <div class="hint">
        按主机 MAC 匹配，IP 随 DHCP 变化时依然命中；前缀至少 3 字节。
        <template v-if="hostBound">主机入口会自动填入本机 MAC，可再追加其它前缀。</template>
      </div>
    </label>
    <label class="field">
      <span>协议</span>
      <select v-model="form.protocol" class="select">
        <option value="any">任意</option>
        <option value="tcp">TCP</option>
        <option value="udp">UDP</option>
      </select>
    </label>
  </div>

  <div class="grid cols-2">
    <label class="field">
      <span>端口范围</span>
      <input v-model="form.port_ranges" class="input" placeholder="3000-3010,8080,9000" />
    </label>
    <label class="field">
      <span>排除端口</span>
      <input v-model="form.exclude_ranges" class="input" placeholder="22,3389" />
    </label>
  </div>

  <div class="grid cols-3">
    <label class="field">
      <span>最小端口</span>
      <input v-model.number="form.min_port" class="input" type="number" />
    </label>
    <label class="field">
      <span>最大端口</span>
      <input v-model.number="form.max_port" class="input" type="number" />
    </label>
    <label class="field">
      <span>自动动作</span>
      <select v-model="form.auto_action" class="select">
        <option v-for="a in ACTIONS" :key="a.value" :value="a.value">{{ a.label }}</option>
      </select>
    </label>
  </div>

  <label class="field row" style="gap: 10px">
    <Switch v-model="form.auto_add" />
    <span class="small dim">命中时自动加入映射列表（等同于「自动添加」）</span>
  </label>

  <label class="field">
    <span>要求进程名包含（可选）</span>
    <input v-model="form.require_name" class="input" placeholder="如 nginx；留空不限制" />
  </label>

  <label class="field">
    <span>命中后附加标签</span>
    <TagInput v-model="form.tags" />
  </label>

  <label class="field">
    <span>备注</span>
    <input v-model="form.note" class="input" placeholder="可选" />
  </label>
</template>
