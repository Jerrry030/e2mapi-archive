import { useState } from 'react'
import { Button, Empty, Input, Space, Typography } from 'antd'
import { CloseOutlined, DownOutlined, UpOutlined } from '@ant-design/icons'
import { t } from '../i18n'

// Provider inference is a display concern only: it colours the chip badge so a
// long model list stays scannable. Unknown models fall back to an initial.
const PROVIDER_RULES: { match: RegExp; name: string; color: string }[] = [
  { match: /^(gpt|chatgpt|o[134]\b|o[134]-|dall-e|whisper|tts-|text-embedding|codex)/, name: 'OpenAI', color: '#10a37f' },
  { match: /claude|anthropic/, name: 'Claude', color: '#d97757' },
  { match: /gemini|gemma|learnlm|imagen|veo-/, name: 'Gemini', color: '#4285f4' },
  { match: /deepseek/, name: 'DeepSeek', color: '#4d6bfe' },
  { match: /qwen|tongyi/, name: 'Qwen', color: '#615ced' },
  { match: /glm|zhipu/, name: 'GLM', color: '#3859ff' },
  { match: /grok|xai/, name: 'Grok', color: '#1d1d1f' },
  { match: /kimi|moonshot/, name: 'Kimi', color: '#16191e' },
  { match: /doubao/, name: 'Doubao', color: '#2b5fd9' },
  { match: /llama/, name: 'Llama', color: '#0668e1' },
  { match: /minimax/, name: 'MiniMax', color: '#f23f5d' },
  { match: /mistral/, name: 'Mistral', color: '#fa520f' },
]

function providerBadge(model: string) {
  const rule = PROVIDER_RULES.find((candidate) => candidate.match.test(model.trim().toLowerCase()))
  return {
    color: rule?.color ?? '#8b5cf6',
    initial: (rule?.name ?? model).charAt(0).toUpperCase(),
    title: rule?.name,
  }
}

const COLLAPSED_LIMIT = 10

export interface ModelWhitelistPanelProps {
  value?: string[]
  onChange?: (models: string[]) => void
  /** Rendered between the chip box and the custom-name input (action buttons). */
  actions?: React.ReactNode
  disabled?: boolean
}

/**
 * ModelWhitelistPanel edits a model whitelist the way sub2api does: a
 * two-column chip grid with provider badges, a count bar that collapses long
 * lists, an action slot, and a custom-name input so upstreams can carry models
 * no suggestion list knows about.
 */
export default function ModelWhitelistPanel({
  value = [],
  onChange,
  actions,
  disabled,
}: ModelWhitelistPanelProps) {
  const [expanded, setExpanded] = useState(false)
  const [customModel, setCustomModel] = useState('')

  const remove = (model: string) => onChange?.(value.filter((item) => item !== model))
  const addCustom = () => {
    const model = customModel.trim()
    if (!model) return
    if (!value.includes(model)) onChange?.([...value, model])
    setCustomModel('')
  }

  const overflowing = value.length > COLLAPSED_LIMIT
  const visible = expanded || !overflowing ? value : value.slice(0, COLLAPSED_LIMIT)

  return (
    <div>
      <div
        style={{
          border: '1px solid var(--ant-color-border, #d9d9d9)',
          borderRadius: 8,
          padding: value.length ? 8 : 0,
        }}
      >
        {value.length ? (
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
              gap: 6,
            }}
          >
            {visible.map((model) => {
              const badge = providerBadge(model)
              return (
                <div
                  key={model}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    gap: 6,
                    background: 'rgba(0,0,0,0.04)',
                    borderRadius: 6,
                    padding: '4px 8px',
                    minWidth: 0,
                  }}
                >
                  <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                    <span
                      title={badge.title}
                      style={{
                        width: 16,
                        height: 16,
                        borderRadius: 4,
                        background: badge.color,
                        color: '#fff',
                        fontSize: 10,
                        lineHeight: '16px',
                        textAlign: 'center',
                        flexShrink: 0,
                      }}
                    >
                      {badge.initial}
                    </span>
                    <Typography.Text
                      ellipsis={{ tooltip: model }}
                      style={{ fontSize: 13, fontFamily: 'monospace' }}
                    >
                      {model}
                    </Typography.Text>
                  </span>
                  {!disabled ? (
                    <Button
                      type="text"
                      size="small"
                      aria-label={t('modelWhitelist.remove', '移除 {model}', { model })}
                      icon={<CloseOutlined style={{ fontSize: 10 }} />}
                      onClick={() => remove(model)}
                    />
                  ) : null}
                </div>
              )
            })}
          </div>
        ) : (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={t('modelWhitelist.empty', '尚未选择模型')}
            style={{ margin: '12px 0' }}
          />
        )}
        {value.length ? (
          <div
            role={overflowing ? 'button' : undefined}
            onClick={overflowing ? () => setExpanded(!expanded) : undefined}
            style={{
              marginTop: value.length ? 8 : 0,
              paddingTop: 6,
              borderTop: '1px dashed rgba(0,0,0,0.1)',
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              cursor: overflowing ? 'pointer' : 'default',
            }}
          >
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('modelWhitelist.count', '{count} 个模型', { count: value.length })}
              {overflowing && !expanded
                ? t('modelWhitelist.collapsedHint', '（已折叠，点击展开）')
                : ''}
            </Typography.Text>
            {overflowing ? (expanded ? <UpOutlined /> : <DownOutlined />) : null}
          </div>
        ) : null}
      </div>

      {actions ? <div style={{ marginTop: 12 }}>{actions}</div> : null}

      <Space.Compact style={{ width: '100%', marginTop: 12 }}>
        <Input
          value={customModel}
          disabled={disabled}
          onChange={(event) => setCustomModel(event.target.value)}
          onPressEnter={addCustom}
          placeholder={t('modelWhitelist.customPlaceholder', '输入自定义模型名称')}
        />
        <Button onClick={addCustom} disabled={disabled || !customModel.trim()}>
          {t('modelWhitelist.fill', '填入')}
        </Button>
      </Space.Compact>
      <Typography.Text type="secondary" style={{ display: 'block', marginTop: 8, fontSize: 12 }}>
        {value.length
          ? t('modelWhitelist.selected', '已选择 {count} 个模型', { count: value.length })
          : t('modelWhitelist.noneSelected', '未选择模型时无法保存；该上游必须声明可承接的模型。')}
      </Typography.Text>
    </div>
  )
}
