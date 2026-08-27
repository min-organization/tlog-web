<script setup lang="ts">
import { ref, nextTick, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { openReplayWS, getToken } from '../api'

const { t } = useI18n()

const props = defineProps<{
  visible: boolean
  rec: string
}>()

const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
}>()

const speed = ref(1)
const termRef = ref<HTMLElement | null>(null)
let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null

function fit() {
  try {
    fitAddon?.fit()
  } catch (e) {
    // 容器尚未挂载或尺寸为 0 时忽略
  }
}

function ensureTerm() {
  if (term || !termRef.value) return
  term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Menlo, Consolas, "DejaVu Sans Mono", monospace',
    scrollback: 10000,
    theme: { background: '#1e1e1e' },
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(termRef.value)
  fit()
}

function closeWS() {
  if (ws) {
    ws.onmessage = null
    ws.onclose = null
    ws.onerror = null
    ws.close()
    ws = null
  }
}

// 建立回放 WebSocket 连接（可重复调用：每次重放都新建）
function connect() {
  closeWS()
  term?.reset()
  term?.writeln(t('replay.connecting'))
  ws = openReplayWS(props.rec, speed.value)
  ws.binaryType = 'arraybuffer' // 确保二进制帧以 ArrayBuffer 接收（避免默认 Blob）
  const decoder = new TextDecoder('utf-8')
  ws.onmessage = async (ev) => {
    let text: string
    if (typeof ev.data === 'string') {
      text = ev.data
    } else {
      // 二进制：ArrayBuffer（binaryType='arraybuffer'）或 Blob（兜底），统一按 UTF-8 流式解码为字符串
      let buf: ArrayBuffer
      if (ev.data instanceof ArrayBuffer) {
        buf = ev.data
      } else {
        buf = await (ev.data as Blob).arrayBuffer()
      }
      // stream:true 跨帧累积，避免多字节 UTF-8 字符被切断产生乱码
      text = decoder.decode(buf, { stream: true })
    }
    term?.write(text)
  }
  ws.onclose = () => {
    // 若令牌已失效(401 已被 api 层清空 token),提示重新登录而非仅“回放结束”
    if (!getToken()) {
      term?.writeln('\r\n' + t('replay.tokenExpired'))
      return
    }
    term?.writeln('\r\n' + t('replay.ended'))
  }
  ws.onerror = () => {
    term?.writeln('\r\n' + t('replay.connError'))
  }
}

function startReplay() {
  ensureTerm()
  connect()
}

function restart() {
  // 重新回放：清屏并重建连接
  startReplay()
}

function stopReplay() {
  closeWS()
  if (term) {
    term.dispose()
    term = null
    fitAddon = null
  }
}

function close() {
  emit('update:visible', false)
}

function onResize() {
  fit()
}

// 对话框打开时初始化终端与 WS；关闭时清理
watch(
  () => props.visible,
  async (v) => {
    if (v) {
      await nextTick()
      startReplay()
      window.addEventListener('resize', onResize)
    } else {
      window.removeEventListener('resize', onResize)
      stopReplay()
    }
  }
)

// 调速即时生效：改变速度滑块时若正在回放则自动重连（重启回放以新速度播放）
watch(speed, () => {
  if (props.visible) restart()
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  stopReplay()
})
</script>

<template>
  <el-dialog
    :model-value="visible"
    :title="`${t('replay.title')}${rec ? ' · ' + rec : ''}`"
    width="82%"
    top="4vh"
    class="replay-dialog"
    @update:model-value="(v: boolean) => emit('update:visible', v)"
    @close="stopReplay"
  >
    <div class="replay-body">
      <div class="replay-toolbar">
        <span>{{ t('replay.speed') }}</span>
        <el-slider v-model="speed" :min="0.1" :max="8" :step="0.1" class="speed-slider" />
        <span class="speed-val">{{ speed }}x</span>
        <el-button size="small" type="primary" @click="restart">{{ t('replay.restart') }}</el-button>
      </div>
      <div ref="termRef" class="xterm-host"></div>
    </div>
    <template #footer>
      <el-button @click="close">{{ t('replay.close') }}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
/* 弹窗整体：纵向 flex，让终端撑满中间区域 */
.replay-body {
  display: flex;
  flex-direction: column;
  height: 82vh;
}
.replay-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
  flex-shrink: 0;
}
.speed-slider {
  width: 200px;
}
.speed-val {
  min-width: 38px;
  text-align: right;
  color: #909399;
  font-size: 13px;
}
/* 终端容器：弹性填充，min-height:0 保证 flex 子项可收缩 */
.xterm-host {
  flex: 1 1 auto;
  min-height: 0;
  width: 100%;
  background: #1e1e1e;
  padding: 8px;
  border-radius: 4px;
  overflow: hidden;
}
</style>

<style>
/* xterm 滚动条美化（非 scoped，因 xterm 渲染到内部 DOM） */
.replay-dialog .xterm-viewport::-webkit-scrollbar {
  width: 10px;
  height: 10px;
}
.replay-dialog .xterm-viewport::-webkit-scrollbar-track {
  background: transparent;
}
.replay-dialog .xterm-viewport::-webkit-scrollbar-thumb {
  background: rgba(255, 255, 255, 0.22);
  border-radius: 5px;
  border: 2px solid transparent;
  background-clip: content-box;
}
.replay-dialog .xterm-viewport::-webkit-scrollbar-thumb:hover {
  background: rgba(255, 255, 255, 0.38);
  background-clip: content-box;
}
.replay-dialog .xterm-viewport {
  scrollbar-width: thin;
  scrollbar-color: rgba(255, 255, 255, 0.22) transparent;
}
</style>
