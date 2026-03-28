import { useList } from "@refinedev/core";
import { List } from "@refinedev/antd";
import {
  Table,
  Space,
  Button,
  Switch,
  Modal,
  Form,
  Input,
  Select,
  Popconfirm,
  message,
} from "antd";
import { useState } from "react";
import axios from "axios";
import { consoleApiUrl } from "../../config";

interface BlacklistKeyword {
  keyword_id: string;
  keyword: string;
  enabled: boolean;
  created_at: number;
}

interface KeywordMutationResp {
  code: number;
  msg: string;
}

export const BlacklistKeywordList = () => {
  const [messageApi, contextHolder] = message.useMessage();
  const [current, setCurrent] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [enabledFilter, setEnabledFilter] = useState<boolean | undefined>(undefined);
  const [createOpen, setCreateOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [createForm] = Form.useForm<{ keyword: string }>();

  const { query } = useList<BlacklistKeyword>({
    resource: "blacklist-keywords",
    pagination: { currentPage: current, pageSize, mode: "server" },
    filters: enabledFilter !== undefined
      ? [{ field: "enabled", operator: "eq" as const, value: enabledFilter }]
      : [],
  });

  const handleToggle = async (record: BlacklistKeyword, checked: boolean) => {
    try {
      const { data } = await axios.put<KeywordMutationResp>(
        `${consoleApiUrl}/blacklist-keywords/${record.keyword_id}`,
        { enabled: checked }
      );
      if (data.code !== 0) {
        messageApi.error(data.msg || "Update failed");
        return;
      }
      messageApi.success("Updated");
      await query.refetch();
    } catch {
      messageApi.error("Update failed");
    }
  };

  const handleCreate = async () => {
    const values = await createForm.validateFields();
    setSubmitting(true);
    try {
      const { data } = await axios.post<KeywordMutationResp>(
        `${consoleApiUrl}/blacklist-keywords`,
        { keyword: values.keyword.trim() }
      );
      if (data.code !== 0) {
        messageApi.error(data.msg || "Create failed");
        return;
      }
      messageApi.success("Created");
      setCreateOpen(false);
      createForm.resetFields();
      await query.refetch();
    } catch {
      messageApi.error("Create failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (keywordId: string) => {
    try {
      const { data } = await axios.delete<KeywordMutationResp>(
        `${consoleApiUrl}/blacklist-keywords/${keywordId}`
      );
      if (data.code !== 0) {
        messageApi.error(data.msg || "Delete failed");
        return;
      }
      messageApi.success("Deleted");
      await query.refetch();
    } catch {
      messageApi.error("Delete failed");
    }
  };

  return (
    <>
      {contextHolder}
      <List
        headerButtons={
          <Space wrap>
            <Button
              type="primary"
              onClick={() => setCreateOpen(true)}
            >
              Add Keyword
            </Button>
            <Select
              placeholder="Filter by status"
              allowClear
              style={{ width: 160 }}
              value={enabledFilter}
              options={[
                { label: "Enabled", value: true },
                { label: "Disabled", value: false },
              ]}
              onChange={(val) => {
                setEnabledFilter(val);
                setCurrent(1);
              }}
            />
          </Space>
        }
      >
        <Table
          dataSource={query.data?.data}
          loading={query.isLoading}
          rowKey="keyword_id"
          pagination={{
            current,
            pageSize,
            total: query.data?.total ?? 0,
            onChange: (p, ps) => {
              setCurrent(p);
              setPageSize(ps);
            },
            showSizeChanger: true,
            pageSizeOptions: [10, 20, 50, 100],
            showTotal: (total) => `Total ${total}`,
          }}
        >
          <Table.Column title="Keyword ID" dataIndex="keyword_id" width={120} />
          <Table.Column title="Keyword" dataIndex="keyword" />
          <Table.Column
            title="Enabled"
            dataIndex="enabled"
            width={100}
            render={(val: boolean, record: BlacklistKeyword) => (
              <Switch
                checked={val}
                onChange={(checked) => void handleToggle(record, checked)}
              />
            )}
          />
          <Table.Column
            title="Created At"
            dataIndex="created_at"
            width={180}
            render={(val: number) => (val ? new Date(val).toLocaleString() : "-")}
          />
          <Table.Column
            title="Actions"
            width={100}
            render={(_: unknown, record: BlacklistKeyword) => (
              <Popconfirm
                title="Delete this keyword?"
                onConfirm={() => void handleDelete(record.keyword_id)}
              >
                <Button type="link" danger>Delete</Button>
              </Popconfirm>
            )}
          />
        </Table>
      </List>

      <Modal
        title="Add Blacklist Keyword"
        open={createOpen}
        onCancel={() => {
          setCreateOpen(false);
          createForm.resetFields();
        }}
        onOk={() => void handleCreate()}
        okButtonProps={{ loading: submitting }}
        destroyOnHidden
      >
        <Form form={createForm} layout="vertical">
          <Form.Item
            name="keyword"
            label="Keyword"
            rules={[{ required: true, message: "Please enter a keyword" }]}
          >
            <Input placeholder="Enter keyword to blacklist" />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
