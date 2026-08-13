<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Session, QueryParams } from '../types'
import { fetchSessions } from '../api'

const { t } = useI18n()

const props = defineProps<{
  params: QueryParams
}>()

const emit = defineEmits<{
  (e: 'play', rec: string): void
  (e: 'update:params', params: QueryParams): void
}>()

const loading = ref(false)
const sessions = ref<Session[]>([])
const total = ref(0)
const currentPage = ref(1)

async function load(p: QueryParams) {
  loading.value = true
  try {
    const resp = await fetchSessions(p)
    sessions.value = resp.items
    total.value = resp.total
    currentPage.value = resp.page
  } catch (e) {
    console.error('load sessions failed', e)
  } finally {
    loading.value = false
  }
}

// 父组件（查询/重置）改变 params 时重新拉取
watch(
  () => props.params,
  (p) => load(p),
  { immediate: true, deep: true }
)

function onPageChange(page: number) {
  // 直接以新页码重新拉取；不向上 emit update:params，避免父级 watch 再次触发重复请求
  load({ ...props.params, page })
}

function onPlay(row: Session) {
  emit('play', row.rec)
}
</script>

<template>
  <div v-loading="loading">
    <el-table :data="sessions" stripe style="width: 100%" @row-dblclick="(r: Session) => onPlay(r)">
      <el-table-column type="index" label="#" width="60" />
      <el-table-column prop="time" :label="t('table.time')" width="180" />
      <el-table-column prop="user" :label="t('table.user')" width="140" />
      <el-table-column prop="summary" :label="t('table.summary')" min-width="300" show-overflow-tooltip />
      <el-table-column prop="rec" :label="t('table.rec')" width="320" show-overflow-tooltip />
      <el-table-column :label="t('table.action')" width="120" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="onPlay(row)">{{ t('table.replay') }}</el-button>
        </template>
      </el-table-column>
    </el-table>

    <div class="pager">
      <el-pagination
        background
        layout="total, prev, pager, next"
        :total="total"
        :page-size="props.params.page_size"
        :current-page="currentPage"
        @current-change="onPageChange"
      />
    </div>
  </div>
</template>

<style scoped>
.pager {
  display: flex;
  justify-content: flex-end;
  padding: 12px 8px;
}
</style>
