<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { login } from '../api'

const { t } = useI18n()

const emit = defineEmits<{
  (e: 'logged-in'): void
}>()

const user = ref('')
const key = ref('')
const loading = ref(false)

async function onSubmit() {
  if (!user.value || !key.value) {
    ElMessage.warning(t('login.warnEmpty'))
    return
  }
  loading.value = true
  try {
    await login(user.value, key.value)
    ElMessage.success(t('login.success'))
    emit('logged-in')
  } catch (e) {
    ElMessage.error((e as Error).message || t('login.error'))
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <el-card class="login-card" shadow="always">
      <template #header>
        <div class="login-header">
          <svg class="logo" viewBox="0 0 24 24" width="26" height="26" aria-hidden="true">
            <rect x="2" y="3" width="20" height="18" rx="2" fill="#1e1e1e" stroke="#409eff" stroke-width="1.5" />
            <rect x="5" y="6.5" width="2.5" height="2.5" rx="0.5" fill="#67c23a" />
            <rect x="9" y="6.5" width="6" height="1.2" rx="0.6" fill="#c0c4cc" />
            <rect x="5" y="11" width="2.5" height="2.5" rx="0.5" fill="#409eff" />
            <rect x="9" y="11" width="8" height="1.2" rx="0.6" fill="#c0c4cc" />
            <rect x="5" y="15" width="2.5" height="2.5" rx="0.5" fill="#e6a23c" />
            <rect x="9" y="15" width="5" height="1.2" rx="0.6" fill="#c0c4cc" />
          </svg>
          <span class="login-title">{{ t('login.title') }}</span>
        </div>
      </template>
      <el-form @submit.prevent="onSubmit">
        <el-form-item :label="t('login.username')">
          <el-input v-model="user" placeholder="admin" autofocus />
        </el-form-item>
        <el-form-item :label="t('login.password')">
          <el-input v-model="key" type="password" :placeholder="t('login.password')" show-password @keyup.enter="onSubmit" />
        </el-form-item>
        <el-button type="primary" :loading="loading" style="width: 100%" @click="onSubmit">{{ t('login.submit') }}</el-button>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #f5f7fa;
}
.login-card {
  width: 360px;
}
.login-header {
  display: flex;
  align-items: center;
  gap: 8px;
  justify-content: center;
}
.login-title {
  font-size: 18px;
  font-weight: 600;
}
</style>
