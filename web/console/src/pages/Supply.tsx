import { useRef, useState } from 'react'
import {
  DrawerForm,
  ModalForm,
  PageContainer,
  ProFormDependency,
  ProFormDigit,
  ProFormSelect,
  ProFormText,
  ProFormTextArea,
} from '@ant-design/pro-components'
import type { ProColumns, ProFormInstance } from '@ant-design/pro-components'
import { Alert, Button, Popconfirm, Tag } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import {
  useAllocateSupplyOffer,
  useActiveRoleUser,
  useCreateSupplyOffer,
  useInstances,
  useRevokeSupplyLedger,
  useRevokeSupplyOffer,
  useSecrets,
  useSupplyLedger,
  useSupplyOffers,
  useUsers,
  useUpdateSupplyOffer,
} from '../api/hooks'
import { EmptyTeach, RelativeTime } from '../components/common'
import { SupplyStatusTag } from '../components/tags'
import { UserSelect } from '../components/fields'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { currentUserId, isPlatformAdmin } from '../api/auth'
import type { CreateSupplyOfferInput } from '../api/endpoints'
import type { SupplyLedgerEntry, SupplyOffer } from '../api/types'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import { labelsFieldValidator, parseLabels as parseLabelsJSON } from './labelsForm'

interface CredentialOption {
  value: string
  label: string
}

interface SupplyOfferFormValues extends CreateSupplyOfferInput {
  labels_text?: string
}

function parseLabels(value?: string): Record<string, string> | undefined {
  return parseLabelsJSON(value, t('supply.form.invalidLabels'))
}

function CredentialSelects({
  upstreamOptions,
  proxyOptions,
  loading = false,
  disabled = false,
}: {
  upstreamOptions: CredentialOption[]
  proxyOptions: CredentialOption[]
  loading?: boolean
  disabled?: boolean
}) {
  const upstreamRefs = new Set(upstreamOptions.map((option) => option.value))
  const proxyRefs = new Set(proxyOptions.map((option) => option.value))
  const placeholder = disabled ? t('supply.selectSupplierFirst') : undefined

  return (
    <>
      <ProFormSelect
        name="credential_ref"
        label={t('supply.form.upstreamCredential')}
        rules={[
          { required: true },
          {
            validator: (_, value) =>
              !value || upstreamRefs.has(value)
                ? Promise.resolve()
                : Promise.reject(new Error(t('supply.invalidCredential'))),
          },
        ]}
        options={upstreamOptions}
        disabled={disabled}
        placeholder={placeholder ?? t('supply.selectCredential')}
        fieldProps={{ loading, showSearch: true, optionFilterProp: 'label' }}
      />
      <ProFormDependency name={['kind']}>
        {({ kind }) => (
          <ProFormSelect
            name="proxy_ref"
            label={t('supply.form.proxyCredential')}
            rules={[
              { required: kind === 'oauth_subscription' },
              {
                validator: (_, value) =>
                  !value || proxyRefs.has(value)
                    ? Promise.resolve()
                    : Promise.reject(new Error(t('supply.invalidProxy'))),
              },
            ]}
            options={proxyOptions}
            disabled={disabled}
            placeholder={placeholder ?? t('supply.selectProxy')}
            fieldProps={{ loading, showSearch: true, optionFilterProp: 'label' }}
          />
        )}
      </ProFormDependency>
    </>
  )
}

function ScopedCredentialSelects({ supplierUserId }: { supplierUserId: number }) {
  const secrets = useSecrets(supplierUserId)
  const scopedSecrets = (secrets.data ?? []).filter(
    (item) => item.user_id === supplierUserId && item.exists,
  )

  return (
    <CredentialSelects
      upstreamOptions={scopedSecrets
        .filter((item) => item.kind === 'upstream')
        .map((item) => ({ value: item.ref, label: item.name }))}
      proxyOptions={scopedSecrets
        .filter((item) => item.kind === 'proxy')
        .map((item) => ({ value: item.ref, label: item.name }))}
      loading={secrets.isFetching}
    />
  )
}

function OffersTab() {
  useLocaleVersion()
  const user = useActiveRoleUser()
  const platform = isPlatformAdmin(user)
  const scopedSupplierId = !platform ? currentUserId(user) : undefined
  const [supplierId, setSupplierId] = useState<number | undefined>()
  const [formSupplierId, setFormSupplierId] = useState<number | undefined>()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<SupplyOffer | null>(null)
  const [allocating, setAllocating] = useState<SupplyOffer | null>(null)
  const effectiveSupplierId = platform ? supplierId : scopedSupplierId
  const credentialSupplierId = platform ? formSupplierId : scopedSupplierId
  const formRef = useRef<ProFormInstance<SupplyOfferFormValues> | undefined>(undefined)
  const { data, isLoading, refetch } = useSupplyOffers(effectiveSupplierId)
  const suppliers = useUsers(platform)
  const supplierUsers = (suppliers.data ?? []).filter(
    (item) => item.enabled && item.roles.includes('supplier'),
  )
  const instances = useInstances(undefined, platform)
  const create = useCreateSupplyOffer()
  const update = useUpdateSupplyOffer()
  const revokeOffer = useRevokeSupplyOffer()
  const allocate = useAllocateSupplyOffer()

  const openCreate = () => {
    setEditing(null)
    setFormSupplierId(platform ? supplierId : scopedSupplierId)
    setOpen(true)
  }

  const openEdit = (offer: SupplyOffer) => {
    setEditing(offer)
    setFormSupplierId(offer.supplier_user_id)
    setOpen(true)
  }

  const handleCreateOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen)
    if (!nextOpen) {
      setEditing(null)
      setFormSupplierId(undefined)
    }
  }

  const columns: ProColumns<SupplyOffer>[] = [
    {
      title: t('supply.columns.provider'),
      dataIndex: 'provider',
      width: 140,
      ellipsis: true,
      render: (v) => v || '-',
    },
    {
      title: t('supply.columns.kind'),
      dataIndex: 'kind',
      width: 170,
      render: (_, r) => (
        <Tag color={r.kind === 'oauth_subscription' ? 'geekblue' : 'cyan'}>
          {t(`supply.kinds.${r.kind}`, r.kind)}
        </Tag>
      ),
    },
    {
      title: t('supply.columns.status'),
      dataIndex: 'status',
      width: 110,
      render: (_, r) => <SupplyStatusTag status={r.status} />,
    },
    { title: t('supply.columns.quota'), dataIndex: 'quota', width: 96, render: (v) => v || '-' },
    {
      title: t('supply.columns.unitPrice'),
      dataIndex: 'unit_price',
      width: 110,
      render: (v) => v || '-',
    },
    {
      title: t('supply.columns.proxyRef'),
      dataIndex: 'proxy_ref',
      width: 220,
      ellipsis: true,
      render: (v, r) =>
        r.kind === 'oauth_subscription'
          ? v || <span style={{ color: '#faad14' }}>{t('supply.proxyRequired')}</span>
          : '-',
    },
    {
      title: t('supply.columns.createdAt'),
      dataIndex: 'created_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.created_at} />,
    },
  ]

  columns.push({
    title: t('supply.columns.actions'),
    valueType: 'option',
    width: platform ? 200 : 130,
    render: (_, r) => {
      if (r.status === 'revoked') return <span style={{ color: '#8c8c8c' }}>-</span>
      return [
        <a key="edit" onClick={() => openEdit(r)}>
          {t('supply.actions.edit')}
        </a>,
        platform ? (
          <a key="allocate" onClick={() => setAllocating(r)}>
            {t('supply.actions.allocate')}
          </a>
        ) : null,
        <Popconfirm
          key="revoke"
          title={t('supply.offerRevokeConfirm')}
          description={t('supply.offerRevokeDescription')}
          onConfirm={() => revokeOffer.mutate(r.id)}
        >
          <a style={{ color: '#ff4d4f' }}>{t('supply.actions.revokeOffer')}</a>
        </Popconfirm>,
      ].filter(Boolean)
    },
  })

  return (
    <>
      <ProTable<SupplyOffer>
        rowKey="id"
        loading={isLoading}
        dataSource={data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        toolBarRender={() => [
          <UserSelect
            key="supplier"
            value={effectiveSupplierId}
            onChange={setSupplierId}
            placeholder={t('common.allAccounts')}
          />,
          <Button key="new" type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            {t('supply.actions.create')}
          </Button>,
        ]}
        locale={{ emptyText: <EmptyTeach title={t('supply.emptyOffers')} /> }}
      />

      <DrawerForm<SupplyOfferFormValues>
        key={editing?.id ?? 'new-offer'}
        title={editing ? t('supply.form.editTitle') : t('supply.form.createTitle')}
        open={open}
        onOpenChange={handleCreateOpenChange}
        formRef={formRef}
        drawerProps={{ destroyOnHidden: true }}
        initialValues={
          editing
            ? {
                supplier_user_id: editing.supplier_user_id,
                kind: editing.kind,
                provider: editing.provider,
                credential_ref: editing.credential_ref,
                proxy_ref: editing.proxy_ref,
                quota: editing.quota,
                unit_price: editing.unit_price,
                labels_text: editing.labels ? JSON.stringify(editing.labels, null, 2) : undefined,
              }
            : platform && supplierId
              ? { supplier_user_id: supplierId }
              : undefined
        }
        onValuesChange={(changedValues) => {
          if (
            !platform ||
            editing ||
            !Object.prototype.hasOwnProperty.call(changedValues, 'supplier_user_id')
          ) {
            return
          }
          formRef.current?.resetFields(['credential_ref', 'proxy_ref'])
          setFormSupplierId(changedValues.supplier_user_id)
        }}
        onFinish={async (values) => {
          const supplierUserId = platform ? values.supplier_user_id : scopedSupplierId
          if (!supplierUserId || supplierUserId !== credentialSupplierId) return false
          const { labels_text: labelsText, ...offerValues } = values
          const body = {
            ...offerValues,
            supplier_user_id: supplierUserId,
            labels: parseLabels(labelsText),
          }
          if (editing) await update.mutateAsync({ id: editing.id, body })
          else await create.mutateAsync(body)
          return true
        }}
      >
        <Alert type="info" message={t('supply.form.notice')} style={{ marginBottom: 16 }} />
        {platform ? (
          <ProFormSelect
            name="supplier_user_id"
            label={t('supply.form.supplier')}
            rules={[{ required: true }]}
            disabled={Boolean(editing)}
            options={supplierUsers.map((item) => ({
              value: item.id,
              label: item.display_name || item.email,
            }))}
          />
        ) : null}
        <ProFormSelect
          name="kind"
          label={t('supply.form.kind')}
          rules={[{ required: true }]}
          options={[
            { value: 'oauth_subscription', label: t('supply.kinds.oauth_subscription') },
            { value: 'api_key', label: t('supply.kinds.api_key') },
          ]}
        />
        <ProFormText
          name="provider"
          label={t('supply.form.provider')}
          placeholder={t('supply.form.providerPlaceholder')}
        />
        {credentialSupplierId ? (
          <ScopedCredentialSelects
            key={credentialSupplierId}
            supplierUserId={credentialSupplierId}
          />
        ) : (
          <CredentialSelects upstreamOptions={[]} proxyOptions={[]} disabled />
        )}
        <ProFormDigit name="quota" label={t('supply.form.quota')} min={0} />
        <ProFormText name="unit_price" label={t('supply.form.unitPrice')} />
        <ProFormTextArea
          name="labels_text"
          label={t('supply.form.labels')}
          placeholder={t('supply.form.labelsPlaceholder')}
          fieldProps={{ rows: 5 }}
          rules={[
            {
              validator: (_: unknown, value?: string) =>
                labelsFieldValidator(_, value).catch(() =>
                  Promise.reject(new Error(t('supply.form.invalidLabels'))),
                ),
            },
          ]}
        />
      </DrawerForm>

      {platform ? (
        <ModalForm
          key={allocating?.id ?? 'alloc'}
          title={t('supply.allocation.title', undefined, {
            name: allocating?.provider || allocating?.id || '',
          })}
          open={!!allocating}
          onOpenChange={(v) => !v && setAllocating(null)}
          modalProps={{ destroyOnHidden: true }}
          onFinish={async (values: { instance_id: string; note?: string }) => {
            if (!allocating) return false
            await allocate.mutateAsync({
              offerId: allocating.id,
              instanceId: values.instance_id,
              note: values.note,
            })
            setAllocating(null)
            return true
          }}
        >
          <Alert type="info" style={{ marginBottom: 16 }} message={t('supply.allocation.notice')} />
          <ProFormSelect
            name="instance_id"
            label={t('supply.allocation.instance')}
            rules={[{ required: true }]}
            options={(instances.data ?? []).map((i) => ({
              value: i.id,
              label: `${i.name} (${i.kind})`,
            }))}
          />
          <ProFormText name="note" label={t('supply.allocation.note')} />
        </ModalForm>
      ) : null}
    </>
  )
}

function LedgerTab() {
  useLocaleVersion()
  const user = useActiveRoleUser()
  const platform = isPlatformAdmin(user)
  const { data, isLoading, refetch } = useSupplyLedger()
  const revoke = useRevokeSupplyLedger()
  const instances = useInstances(undefined, platform)
  const users = useUsers(platform)

  const instName = (id: string) => (instances.data ?? []).find((i) => i.id === id)?.name ?? id
  const userName = (id: number) =>
    (users.data ?? []).find((item) => item.id === id)?.display_name ??
    (users.data ?? []).find((item) => item.id === id)?.email ??
    String(id)

  const columns: ProColumns<SupplyLedgerEntry>[] = [
    { title: t('supply.columns.ledgerId'), dataIndex: 'id', width: 160, ellipsis: true },
    { title: t('supply.columns.offer'), dataIndex: 'offer_id', width: 160, ellipsis: true },
    {
      title: '供给账号',
      dataIndex: 'supplier_user_id',
      width: 140,
      ellipsis: true,
      render: (_, r) => userName(r.supplier_user_id),
    },
    {
      title: '托管账号',
      dataIndex: 'user_id',
      width: 140,
      ellipsis: true,
      render: (_, r) => userName(r.user_id),
    },
    {
      title: t('supply.columns.instance'),
      dataIndex: 'instance_id',
      width: 160,
      ellipsis: true,
      render: (_, r) => instName(r.instance_id),
    },
    {
      title: t('supply.columns.status'),
      dataIndex: 'status',
      width: 110,
      render: (_, r) =>
        r.status === 'allocated' ? (
          <Tag color="success">{t('supply.ledgerStatus.allocated')}</Tag>
        ) : (
          <Tag>{t('supply.ledgerStatus.revoked')}</Tag>
        ),
    },
    {
      title: t('supply.columns.note'),
      dataIndex: 'note',
      width: 180,
      ellipsis: true,
      render: (v) => v || '-',
    },
    {
      title: t('supply.columns.createdAt'),
      dataIndex: 'created_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.created_at} />,
    },
  ]

  if (platform) {
    columns.push({
      title: t('supply.columns.actions'),
      valueType: 'option',
      width: 100,
      render: (_, r) =>
        r.status === 'allocated' ? (
          <Popconfirm
            title={t('supply.revokeConfirm')}
            description={t('supply.revokeDescription')}
            onConfirm={() => revoke.mutate({ ledgerId: r.id })}
          >
            <a style={{ color: '#ff4d4f' }}>{t('supply.actions.revoke')}</a>
          </Popconfirm>
        ) : (
          <span style={{ color: '#ccc' }}>-</span>
        ),
    })
  }

  return (
    <ProTable<SupplyLedgerEntry>
      rowKey="id"
      loading={isLoading}
      dataSource={data ?? []}
      columns={columns}
      search={false}
      scroll={{ x: 'max-content' }}
      options={{ reload: () => refetch() }}
      locale={{ emptyText: <EmptyTeach title={t('supply.emptyLedger')} /> }}
    />
  )
}

export default function Supply() {
  useLocaleVersion()
  const [tab, setTab] = useState('offers')
  return (
    <PageContainer
      title={t('supply.title')}
      subTitle={t('supply.subtitle')}
      tabList={[
        { key: 'offers', tab: t('supply.tabs.offers') },
        { key: 'ledger', tab: t('supply.tabs.ledger') },
      ]}
      tabActiveKey={tab}
      onTabChange={setTab}
    >
      {tab === 'offers' ? <OffersTab /> : <LedgerTab />}
    </PageContainer>
  )
}
