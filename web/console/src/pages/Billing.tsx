import { useState } from 'react'
import { PageContainer, ProCard, StatisticCard } from '@ant-design/pro-components'
import { Alert, DatePicker, Empty, Space, Table, Tag } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import dayjs, { Dayjs } from 'dayjs'
import { useActiveRoleUser, useBillingStatement } from '../api/hooks'
import { UserSelect } from '../components/fields'
import { currentUserId, isPlatformAdmin } from '../api/auth'
import { friendlyErrorMessage } from '../api/errors'
import type { BillingLine } from '../api/types'

const lineColumns: ColumnsType<BillingLine> = [
  { title: '项目', dataIndex: 'item', width: 180, ellipsis: true },
  { title: '数量', dataIndex: 'quantity', width: 100, align: 'right' },
  { title: '单价', dataIndex: 'unit_price', width: 120, align: 'right' },
  { title: '金额', dataIndex: 'amount', width: 120, align: 'right', render: (v) => <b>{v}</b> },
  { title: '说明', dataIndex: 'note', width: 240, ellipsis: true, render: (v) => v || '—' },
]

export default function Billing() {
  const user = useActiveRoleUser()
  const platform = isPlatformAdmin(user)
  const [userId, setUserId] = useState<number | undefined>(
    !platform ? currentUserId(user) : undefined,
  )
  const [month, setMonth] = useState<Dayjs>(dayjs())
  const period = month.format('YYYY-MM')
  const { data, isLoading, isError, error } = useBillingStatement(userId, period)

  return (
    <PageContainer title="账单与费用" subTitle="固定托管费 + 处置费；用量仅参考，不按量分成">
      <Alert
        type="info"
        style={{ marginBottom: 16 }}
        message="计费模型：托管实例数 × 月费 + 处置次数 × 单价。配置下发链路不经数据面，网关自报用量不可信，故不做按量分成（详见架构文档第 4 节）。"
      />
      <Space style={{ marginBottom: 16 }}>
        {platform ? (
          <UserSelect
            value={userId}
            onChange={setUserId}
            placeholder="选择账号"
            allowClear={false}
          />
        ) : null}
        <DatePicker
          picker="month"
          value={month}
          onChange={(v) => v && setMonth(v)}
          allowClear={false}
        />
      </Space>

      {!userId ? (
        <Empty description="选择一个用户查看账单" />
      ) : isError ? (
        <Alert type="error" message={`账单计算失败：${friendlyErrorMessage(error)}`} />
      ) : (
        <>
          <StatisticCard.Group direction="row" style={{ marginBottom: 16 }}>
            <StatisticCard
              loading={isLoading}
              statistic={{ title: '托管实例数', value: data?.instance_count ?? 0 }}
            />
            <StatisticCard.Divider />
            <StatisticCard
              loading={isLoading}
              statistic={{ title: '处置次数（本期）', value: data?.disposition_count ?? 0 }}
            />
            <StatisticCard.Divider />
            <StatisticCard
              loading={isLoading}
              statistic={{
                title: `应付合计（${data?.currency ?? 'CNY'}）`,
                value: data?.total ?? '0.00',
              }}
            />
          </StatisticCard.Group>

          <ProCard
            title={
              <Space>
                账单明细
                <Tag>{data?.period}</Tag>
                {data?.user_email && <Tag color="blue">{data.user_email}</Tag>}
              </Space>
            }
            loading={isLoading}
          >
            <Table<BillingLine>
              rowKey="item"
              size="small"
              pagination={false}
              columns={lineColumns}
              dataSource={data?.lines ?? []}
              scroll={{ x: 'max-content' }}
              summary={() => (
                <Table.Summary.Row>
                  <Table.Summary.Cell index={0} colSpan={3}>
                    <b>合计</b>
                  </Table.Summary.Cell>
                  <Table.Summary.Cell index={1} align="right">
                    <b>
                      {data?.total} {data?.currency}
                    </b>
                  </Table.Summary.Cell>
                  <Table.Summary.Cell index={2} />
                </Table.Summary.Row>
              )}
            />
          </ProCard>
        </>
      )}
    </PageContainer>
  )
}
