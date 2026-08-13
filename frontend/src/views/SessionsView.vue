<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import FilterBar from '../components/FilterBar.vue'
import SessionTable from '../components/SessionTable.vue'
import ReplayDialog from '../components/ReplayDialog.vue'
import type { QueryParams } from '../types'

const { t } = useI18n()

const params = ref<QueryParams>({
  page: 1,
  page_size: 50,
  user: '',
  q: '',
  date_from: '',
  date_to: '',
})

const replayVisible = ref(false)
const replayRec = ref('')

function onSearch(p: QueryParams) {
  params.value = p
}

function onReset() {
  params.value = { page: 1, page_size: 50, user: '', q: '', date_from: '', date_to: '' }
}

function onPlay(rec: string) {
  replayRec.value = rec
  replayVisible.value = true
}
</script>

<template>
  <div class="sessions-view">
    <el-card shadow="never">
      <template #header>
        <div class="header">
          <span class="title">{{ t('sessions.title') }}</span>
          <el-button size="small" @click="onReset">{{ t('sessions.refresh') }}</el-button>
        </div>
      </template>
      <FilterBar @search="onSearch" @reset="onReset" />
      <SessionTable :params="params" @play="onPlay" @update:params="(p) => (params = p)" />
    </el-card>

    <ReplayDialog :visible="replayVisible" :rec="replayRec" @update:visible="(v) => (replayVisible = v)" />
  </div>
</template>

<style scoped>
.sessions-view {
  padding: 16px;
  max-width: 1400px;
  margin: 0 auto;
}
.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.title {
  font-size: 18px;
  font-weight: 600;
}
</style>
