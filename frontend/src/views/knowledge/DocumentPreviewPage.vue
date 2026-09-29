<template>
  <div class="document-preview-page">
    <div class="document-preview-page__bar">
      <t-button theme="default" variant="text" size="small" @click="goBack">
        <template #icon>
          <t-icon name="chevron-left" />
        </template>
        {{ $t('common.back') }}
      </t-button>
      <span class="document-preview-page__name" :title="fileName">{{ fileName || $t('preview.tab') }}</span>
    </div>
    <div class="document-preview-page__body">
      <DocumentPreview
        v-if="knowledgeId"
        :knowledgeId="knowledgeId"
        :fileType="fileType"
        :fileName="fileName"
        :active="true"
        :page="page"
      />
      <div v-else class="document-preview-page__error">{{ $t('preview.loadFailed') }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import DocumentPreview from '@/components/document-preview.vue'

const route = useRoute()
const router = useRouter()

const knowledgeId = computed(() => String(route.query.knowledgeId || ''))
const fileType = computed(() => String(route.query.fileType || 'pdf'))
const fileName = computed(() => String(route.query.fileName || ''))
const page = computed(() => {
  const n = Number(route.query.page)
  return Number.isFinite(n) && n > 0 ? n : 0
})

const goBack = () => {
  if (window.history.length > 1) {
    router.back()
  } else {
    router.push('/platform/knowledge-bases')
  }
}
</script>

<style scoped lang="less">
.document-preview-page {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  padding: 12px 16px 16px;
  box-sizing: border-box;
  background: var(--td-bg-color-page);

  &__bar {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
    min-height: 32px;
  }

  &__name {
    font-size: 14px;
    font-weight: 500;
    color: var(--td-text-color-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  &__body {
    flex: 1;
    min-height: 0;
  }

  &__error {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 60px 20px;
    color: var(--td-text-color-secondary);
  }
}
</style>
