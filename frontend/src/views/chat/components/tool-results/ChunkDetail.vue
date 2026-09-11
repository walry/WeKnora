<template>
  <div class="chunk-detail">
    <div class="info-section">
      <div class="info-field">
        <span class="field-label">{{ $t('chat.chunkIdLabel') }}</span>
        <span class="field-value"><code>{{ data.chunk_id }}</code></span>
      </div>
      <div class="info-field">
        <span class="field-label">{{ $t('chat.documentIdLabel') }}</span>
        <span class="field-value"><code>{{ data.knowledge_id }}</code></span>
      </div>
      <div class="info-field">
        <span class="field-label">{{ $t('chat.positionLabel') }}</span>
        <span class="field-value">{{ $t('chat.chunkPositionValue', { index: data.chunk_index }) }}</span>
      </div>
      <div v-if="pageLabel" class="info-field">
        <span class="field-label">{{ $t('chat.sourcePageLabel') }}</span>
        <span class="field-value">{{ pageLabel }}</span>
      </div>
      <div v-if="data.content_length" class="info-field">
        <span class="field-label">{{ $t('chat.contentLengthLabelSimple') }}</span>
        <span class="field-value">{{ $t('chat.lengthChars', { value: data.content_length }) }}</span>
      </div>
    </div>

    <div class="info-section">
      <div class="info-section-title">{{ $t('chat.fullContentLabel') }}</div>
      <div class="full-content">{{ data.content }}</div>
    </div>

    <div class="info-section">
      <div class="action-buttons">
        <button class="action-button" @click="copyToClipboard">
          📋 {{ $t('chat.copyContent') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineProps } from 'vue';
import type { ChunkDetailData } from '@/types/tool-results';
import { useI18n } from 'vue-i18n';

const props = defineProps<{
  data: ChunkDetailData;
}>();

const { t } = useI18n();

// "第 3 页" for a single page, "第 3-4 页" when the chunk spans pages; empty
// when the page is unknown (page_start <= 0), so the UI can hide the field.
const pageLabel = computed(() => {
  const { page_start, page_end } = props.data;
  if (!page_start || page_start <= 0) return '';
  const end = page_end && page_end > page_start ? page_end : page_start;
  return end > page_start
    ? t('chat.pageRangeValue', { start: page_start, end })
    : t('chat.pagePositionValue', { page: page_start });
});

const copyToClipboard = () => {
  const text = props.data.content;
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).catch(() => {
      fallbackCopy(text);
    });
  } else {
    fallbackCopy(text);
  }
};

function fallbackCopy(text: string) {
  const textArea = document.createElement('textarea');
  textArea.value = text;
  textArea.style.position = 'fixed';
  textArea.style.opacity = '0';
  document.body.appendChild(textArea);
  textArea.select();
  document.execCommand('copy');
  document.body.removeChild(textArea);
}
</script>

<style lang="less" scoped>
@import './tool-results.less';

.chunk-detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 8px 0;
}

code {
  font-family: var(--app-font-family-mono);
  font-size: 11px;
  background: var(--td-bg-color-secondarycontainer);
  padding: 2px 4px;
  border-radius: 3px;
}

.action-buttons {
  display: flex;
  gap: 8px;
}
</style>

