<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { fetchUsers } from '../api'
import type { QueryParams } from '../types'

const { t } = useI18n()

const emit = defineEmits<{
  (e: 'search', params: QueryParams): void
  (e: 'reset'): void
}>()

const user = ref('')
const q = ref('')
const dateFrom = ref('')
const dateTo = ref('')
const pageSize = ref(50)
const userOptions = ref<string[]>([])

onMounted(async () => {
  try {
    userOptions.value = await fetchUsers()
  } catch (e) {
    console.warn('load users failed', e)
  }
})

function buildParams(): QueryParams {
  return {
    page: 1,
    page_size: pageSize.value,
    user: user.value,
    q: q.value,
    date_from: dateFrom.value,
    date_to: dateTo.value,
  }
}

function onSearch() {
  emit('search', buildParams())
}

function onReset() {
  user.value = ''
  q.value = ''
  dateFrom.value = ''
  dateTo.value = ''
  pageSize.value = 50
  emit('reset')
}
</script>

<template>
  <el-form :inline="true" class="filter-bar" @submit.prevent>
    <el-form-item :label="t('filter.user')">
      <el-select v-model="user" :placeholder="t('filter.userPlaceholder')" clearable style="width: 150px">
        <el-option v-for="u in userOptions" :key="u" :label="u" :value="u" />
      </el-select>
    </el-form-item>
    <el-form-item :label="t('filter.dateFrom')">
      <el-date-picker v-model="dateFrom" type="date" value-format="YYYY-MM-DD" :placeholder="t('filter.dateFromPlaceholder')" />
    </el-form-item>
    <el-form-item :label="t('filter.dateTo')">
      <el-date-picker v-model="dateTo" type="date" value-format="YYYY-MM-DD" :placeholder="t('filter.dateToPlaceholder')" />
    </el-form-item>
    <el-form-item :label="t('filter.search')">
      <el-input v-model="q" :placeholder="t('filter.searchPlaceholder')" clearable style="width: 200px" @keyup.enter="onSearch" />
    </el-form-item>
    <el-form-item :label="t('filter.pageSize')">
      <el-select v-model="pageSize" style="width: 100px">
        <el-option :value="20" label="20" />
        <el-option :value="50" label="50" />
        <el-option :value="100" label="100" />
      </el-select>
    </el-form-item>
    <el-form-item>
      <el-button type="primary" @click="onSearch">{{ t('filter.query') }}</el-button>
      <el-button @click="onReset">{{ t('filter.reset') }}</el-button>
    </el-form-item>
  </el-form>
</template>

<style scoped>
.filter-bar {
  padding: 12px 8px 0;
}
</style>
