<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SessionsView from './views/SessionsView.vue'
import LoginView from './views/LoginView.vue'
import { getToken, clearToken, getUsername, logout } from './api'
import { setLocale } from './i18n'

const { t, locale } = useI18n()
const loggedIn = ref(!!getToken())
const username = ref(getUsername())

function onLoggedIn() {
  loggedIn.value = true
  username.value = getUsername()
}

async function onLogout() {
  await logout()
  loggedIn.value = false
  username.value = ''
}

function onLangChange(val: 'zh' | 'en') {
  setLocale(val)
}
</script>

<template>
  <el-config-provider>
    <div v-if="loggedIn">
      <header class="topbar">
        <div class="brand">
          <!-- 内联 SVG 终端 logo（自包含，无外部图片依赖） -->
          <svg class="logo" viewBox="0 0 24 24" width="22" height="22" aria-hidden="true">
            <rect x="2" y="3" width="20" height="18" rx="2" fill="#1e1e1e" stroke="#409eff" stroke-width="1.5" />
            <rect x="5" y="6.5" width="2.5" height="2.5" rx="0.5" fill="#67c23a" />
            <rect x="9" y="6.5" width="6" height="1.2" rx="0.6" fill="#c0c4cc" />
            <rect x="5" y="11" width="2.5" height="2.5" rx="0.5" fill="#409eff" />
            <rect x="9" y="11" width="8" height="1.2" rx="0.6" fill="#c0c4cc" />
            <rect x="5" y="15" width="2.5" height="2.5" rx="0.5" fill="#e6a23c" />
            <rect x="9" y="15" width="5" height="1.2" rx="0.6" fill="#c0c4cc" />
          </svg>
          <span class="brand-text">{{ t('brand') }}</span>
        </div>
        <div class="topbar-right">
          <el-select :model-value="locale" @change="onLangChange" size="small" class="lang-select">
            <el-option label="中文" value="zh" />
            <el-option label="EN" value="en" />
          </el-select>
          <span class="user">{{ username || 'admin' }}</span>
          <el-button size="small" @click="onLogout">{{ t('topbar.logout') }}</el-button>
        </div>
      </header>
      <SessionsView />
    </div>
    <LoginView v-else @logged-in="onLoggedIn" />
  </el-config-provider>
</template>

<style scoped>
.topbar {
  position: sticky;
  top: 0;
  z-index: 100;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 16px;
  background: #fff;
  border-bottom: 1px solid #ebeef5;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.04);
}
.brand {
  display: flex;
  align-items: center;
  gap: 8px;
}
.logo {
  flex-shrink: 0;
}
.brand-text {
  font-weight: 600;
  font-size: 15px;
}
.topbar-right {
  display: flex;
  align-items: center;
  gap: 12px;
}
.lang-select {
  width: 92px;
}
.user {
  color: #606266;
  font-size: 13px;
}
</style>
