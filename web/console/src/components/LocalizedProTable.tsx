import { ProTable } from '@ant-design/pro-components'
import type { ParamsType, ProTableProps } from '@ant-design/pro-components'

export function LocalizedProTable<
  DataType extends Record<string, any>,
  Params extends ParamsType = ParamsType,
  ValueType = 'text',
>(props: ProTableProps<DataType, Params, ValueType>) {
  const pagination =
    props.pagination === false
      ? false
      : {
          ...props.pagination,
          showSizeChanger: false,
        }

  return <ProTable<DataType, Params, ValueType> {...props} pagination={pagination} />
}
