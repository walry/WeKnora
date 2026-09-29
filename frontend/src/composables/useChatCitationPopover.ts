import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getChunkByIdOnly } from '@/api/knowledge-base'
import { getEmbedChunkById } from '@/api/embed'
import { resolveCitationChunkId, type CitationKnowledgeRef } from '@/utils/citationMarkdown'
import {
  getCitationChunkCache,
  setCitationChunkCache,
} from '@/utils/citationChunkCache'

export { clearCitationChunkCache } from '@/utils/citationChunkCache'

type FloatState = {
  visible: boolean
  type: 'kb' | 'web'
  top: number
  left: number
  title: string
  content: string
  url: string
  loading: boolean
  error: string
  /** 1-based source page of the cited chunk (0 = unknown). */
  page: number
  /** Parent knowledge/document ID of the cited chunk. */
  docId: string
  /** Whether a source-document preview link should be shown. */
  canPreview: boolean
  /** New-tab URL that opens the source document at `page`. */
  previewUrl: string
}

export type CitationFloatState = FloatState

export type ChatCitationPopoverOptions = {
  getKnowledgeReferences?: () => CitationKnowledgeRef[] | null | undefined
  embedChannelId?: () => string | undefined
  embedToken?: () => string | undefined
  sessionId?: () => string | undefined
}

export function useChatCitationPopover(
  rootRef: Ref<HTMLElement | null>,
  options?: ChatCitationPopoverOptions,
) {
  const { t } = useI18n()

  const getCacheScope = () => {
    const channelId = options?.embedChannelId?.()
    const token = options?.embedToken?.()
    if (channelId && token) return `embed:${channelId}:${token}`
    return options?.sessionId?.() || 'default'
  }

  const float = ref<FloatState>({
    visible: false,
    type: 'kb',
    top: 0,
    left: 0,
    title: '',
    content: '',
    url: '',
    loading: false,
    error: '',
    page: 0,
    docId: '',
    canPreview: false,
    previewUrl: '',
  })

  let hoverTimer: number | null = null
  let closeTimer: number | null = null

  const positionFor = (el: HTMLElement, offsetY = 0) => {
    const rect = el.getBoundingClientRect()
    float.value.top = rect.bottom + window.scrollY + 6 + offsetY
    float.value.left = Math.min(rect.left + window.scrollX, window.innerWidth - 320)
  }

  const resetSourceLink = () => {
    float.value.page = 0
    float.value.docId = ''
    float.value.canPreview = false
    float.value.previewUrl = ''
  }

  const openWeb = (el: HTMLElement) => {
    const url = el.getAttribute('data-url') || ''
    float.value.type = 'web'
    float.value.url = url
    float.value.title = el.querySelector('.tip-title')?.textContent || ''
    float.value.content = ''
    float.value.loading = false
    float.value.error = ''
    resetSourceLink()
    float.value.visible = true
    positionFor(el)
  }

  const isEmbedMode = () => {
    return !!(options?.embedChannelId?.() && options?.embedToken?.())
  }

  /** Extension-derived PDF check against the retrieval references. */
  const isPdfDocument = (docId: string, doc: string): boolean => {
    const refs = (options?.getKnowledgeReferences?.() || []).filter(
      (r) => r && r.chunk_type !== 'web_search',
    )
    if (!refs.length) return false

    let hit = docId ? refs.find((r) => r.knowledge_id === docId) : undefined
    if (!hit && doc) {
      const target = doc.trim().toLowerCase()
      hit = refs.find((r) => {
        const title = (r.knowledge_title || '').trim().toLowerCase()
        const filename = (r.knowledge_filename || '').trim().toLowerCase()
        for (const name of [title, filename]) {
          if (name && (name === target || name.includes(target) || target.includes(name))) {
            return true
          }
        }
        return false
      })
    }

    const name = (hit?.knowledge_filename || hit?.knowledge_title || '').toLowerCase()
    return name.endsWith('.pdf')
  }

  const applySourceLink = (docId: string, rawPage: unknown) => {
    const page = Number(rawPage) || 0
    float.value.page = page > 0 ? page : 0
    float.value.docId = docId || ''
    const showable =
      !isEmbedMode() && float.value.page > 0 && !!docId && isPdfDocument(docId, float.value.title)
    float.value.canPreview = showable
    if (!showable) {
      float.value.previewUrl = ''
      return
    }
    const params = new URLSearchParams({
      knowledgeId: docId,
      fileType: 'pdf',
      fileName: float.value.title || '',
      page: String(float.value.page),
    })
    float.value.previewUrl = `/platform/document-preview?${params.toString()}`
  }

  const fetchChunkContent = async (chunkId: string) => {
    const channelId = options?.embedChannelId?.()
    const token = options?.embedToken?.()
    if (channelId && token) {
      return getEmbedChunkById(channelId, token, chunkId)
    }
    return getChunkByIdOnly(chunkId)
  }

  const openKb = async (el: HTMLElement) => {
    const rawChunkId = el.getAttribute('data-chunk-id') || ''
    const title = el.getAttribute('data-doc') || ''
    const kbId = el.getAttribute('data-kb-id') || ''
    const chunkId = resolveCitationChunkId(
      rawChunkId,
      { doc: title, kbId },
      options?.getKnowledgeReferences?.(),
    ) || rawChunkId
    if (!chunkId) return
    float.value.type = 'kb'
    float.value.title = title
    float.value.url = ''
    resetSourceLink()
    float.value.visible = true
    positionFor(el, 4)

    const scope = getCacheScope()
    const cached = getCitationChunkCache(scope, chunkId)
    if (cached) {
      float.value.content = cached.content
      float.value.error = cached.error || ''
      float.value.loading = false
      if (cached.docId) {
        applySourceLink(cached.docId, cached.page)
      }
      return
    }

    float.value.loading = true
    float.value.error = ''
    float.value.content = ''
    try {
      const res = await fetchChunkContent(chunkId)
      const content = String(res?.data?.content || '').trim()
      const docId = String(res?.data?.knowledge_id || '')
      const page = Number(res?.data?.page_start) || 0
      if (!content) {
        const msg = t('agentStream.citation.notFound')
        setCitationChunkCache(scope, chunkId, { content: '', error: msg, docId, page })
        float.value.error = msg
        applySourceLink(docId, page)
        return
      }
      setCitationChunkCache(scope, chunkId, { content, docId, page })
      float.value.content = content
      applySourceLink(docId, page)
    } catch {
      const msg = t('agentStream.citation.loadFailed')
      setCitationChunkCache(scope, chunkId, { content: '', error: msg })
      float.value.error = msg
    } finally {
      float.value.loading = false
    }
  }

  const scheduleClose = () => {
    if (closeTimer) window.clearTimeout(closeTimer)
    closeTimer = window.setTimeout(() => {
      const hoveredCitation = document.querySelector('.citation-kb:hover, .citation-web:hover')
      const hoveredPopup = document.querySelector('.chat-citation-float:hover')
      if (!hoveredCitation && !hoveredPopup) {
        float.value.visible = false
      }
    }, 120)
  }

  const cancelClose = () => {
    if (closeTimer) {
      window.clearTimeout(closeTimer)
      closeTimer = null
    }
  }

  const onMouseOver = (e: Event) => {
    const target = e.target as HTMLElement
    const kbEl = target.closest?.('.citation-kb') as HTMLElement | null
    const webEl = target.closest?.('.citation-web') as HTMLElement | null
    if (!kbEl && !webEl) return
    cancelClose()
    if (hoverTimer) window.clearTimeout(hoverTimer)
    hoverTimer = window.setTimeout(() => {
      if (kbEl) void openKb(kbEl)
      else if (webEl) openWeb(webEl)
    }, kbEl ? 80 : 40)
  }

  const onMouseOut = (e: Event) => {
    const rt = (e as MouseEvent).relatedTarget as HTMLElement | null
    if (rt?.closest?.('.citation-kb, .citation-web, .chat-citation-float')) return
    if (hoverTimer) {
      window.clearTimeout(hoverTimer)
      hoverTimer = null
    }
    scheduleClose()
  }

  const onClick = (e: Event) => {
    const target = e.target as HTMLElement
    const kbEl = target.closest?.('.citation-kb') as HTMLElement | null
    if (kbEl) {
      e.preventDefault()
      e.stopPropagation()
      void openKb(kbEl)
    }
  }

  const onViewportChange = () => {
    if (float.value.visible) scheduleClose()
  }

  const bind = () => {
    const root = rootRef.value
    if (!root) return
    root.addEventListener('mouseover', onMouseOver, true)
    root.addEventListener('mouseout', onMouseOut, true)
    root.addEventListener('click', onClick, true)
    window.addEventListener('scroll', onViewportChange, true)
    window.addEventListener('resize', onViewportChange, true)
  }

  const unbind = () => {
    const root = rootRef.value
    if (root) {
      root.removeEventListener('mouseover', onMouseOver, true)
      root.removeEventListener('mouseout', onMouseOut, true)
      root.removeEventListener('click', onClick, true)
    }
    window.removeEventListener('scroll', onViewportChange, true)
    window.removeEventListener('resize', onViewportChange, true)
  }

  watch(rootRef, () => {
    unbind()
    bind()
  }, { flush: 'post' })

  onMounted(() => {
    bind()
  })

  onBeforeUnmount(() => {
    unbind()
    if (hoverTimer) window.clearTimeout(hoverTimer)
    if (closeTimer) window.clearTimeout(closeTimer)
  })

  return { float, rebind: () => { unbind(); bind() }, cancelClose, scheduleClose }
}
