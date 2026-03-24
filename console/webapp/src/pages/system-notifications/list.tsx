import { useList } from "@refinedev/core";
import { List } from "@refinedev/antd";
import {
  Button,
  DatePicker,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import axios from "axios";
import dayjs from "dayjs";
import { useState } from "react";

import { consoleApiUrl } from "../../config";

interface SystemNotification {
  notification_id: string;
  type: string;
  content: string;
  status: number;
  audience_type: string;
  audience_expression: string;
  start_at: number;
  end_at: number;
  offline_at: number;
  created_at: number;
  updated_at: number;
}

interface NotificationMutationResp {
  code: number;
  msg: string;
  data?: {
    notification?: SystemNotification;
  };
}

type CreateFormValues = {
  type: string;
  content: string;
  status: number;
  time_range?: [dayjs.Dayjs, dayjs.Dayjs];
};

type EditFormValues = {
  type: string;
  content: string;
  status: number;
  time_range?: [dayjs.Dayjs, dayjs.Dayjs] | null;
};

const statusMap: Record<number, { label: string; color: string }> = {
  0: { label: "Draft", color: "default" },
  1: { label: "Active", color: "success" },
  2: { label: "Offline", color: "error" },
};

const formatTimestamp = (ts: number) => {
  if (!ts) return "-";
  return new Date(ts).toLocaleString();
};

export const SystemNotificationList = () => {
  const [statusFilter, setStatusFilter] = useState<number | undefined>();
  const [current, setCurrent] = useState<number>(1);
  const [pageSize, setPageSize] = useState<number>(20);
  const [messageApi, contextHolder] = message.useMessage();

  const [createOpen, setCreateOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [editingNotif, setEditingNotif] = useState<SystemNotification | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const [createForm] = Form.useForm<CreateFormValues>();
  const [editForm] = Form.useForm<EditFormValues>();

  const { query } = useList<SystemNotification>({
    resource: "system-notifications",
    pagination: { currentPage: current, pageSize, mode: "server" },
    filters: [
      ...(statusFilter !== undefined
        ? [{ field: "status", operator: "eq" as const, value: statusFilter }]
        : []),
    ],
  });

  const refetch = async () => {
    await query.refetch();
  };

  const handleCreate = async () => {
    const values = await createForm.validateFields();
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        type: values.type,
        content: values.content,
        status: values.status,
      };
      if (values.time_range) {
        body.start_at = values.time_range[0].valueOf();
        body.end_at = values.time_range[1].valueOf();
      }
      const { data } = await axios.post<NotificationMutationResp>(
        `${consoleApiUrl}/system-notifications`,
        body
      );
      if (data.code !== 0) throw new Error(data.msg || "Create failed");
      messageApi.success("Notification created");
      setCreateOpen(false);
      createForm.resetFields();
      await refetch();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Create failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handleEdit = async () => {
    if (!editingNotif) return;
    const values = await editForm.validateFields();
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        type: values.type,
        content: values.content,
        status: values.status,
      };
      if (values.time_range) {
        body.start_at = values.time_range[0].valueOf();
        body.end_at = values.time_range[1].valueOf();
      } else {
        body.start_at = 0;
        body.end_at = 0;
      }
      const { data } = await axios.put<NotificationMutationResp>(
        `${consoleApiUrl}/system-notifications/${editingNotif.notification_id}`,
        body
      );
      if (data.code !== 0) throw new Error(data.msg || "Update failed");
      messageApi.success("Notification updated");
      setEditOpen(false);
      setEditingNotif(null);
      await refetch();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Update failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handleOffline = async (record: SystemNotification) => {
    try {
      const { data } = await axios.post<NotificationMutationResp>(
        `${consoleApiUrl}/system-notifications/${record.notification_id}/offline`
      );
      if (data.code !== 0) throw new Error(data.msg || "Offline failed");
      messageApi.success("Notification offlined");
      await refetch();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Offline failed");
    }
  };

  const openEditModal = (record: SystemNotification) => {
    setEditingNotif(record);
    editForm.setFieldsValue({
      type: record.type,
      content: record.content,
      status: record.status,
      time_range:
        record.start_at && record.end_at
          ? [dayjs(record.start_at), dayjs(record.end_at)]
          : undefined,
    });
    setEditOpen(true);
  };

  const columns: ColumnsType<SystemNotification> = [
    {
      title: "ID",
      dataIndex: "notification_id",
      key: "notification_id",
      width: 120,
      fixed: "left",
    },
    {
      title: "Type",
      dataIndex: "type",
      key: "type",
      width: 120,
      render: (v: string) => <Tag color="blue">{v}</Tag>,
    },
    {
      title: "Content",
      dataIndex: "content",
      key: "content",
      width: 400,
      render: (v: string) => (
        <Typography.Paragraph
          ellipsis={{ rows: 2, expandable: true, symbol: "more" }}
          style={{ marginBottom: 0, maxWidth: 380, whiteSpace: "pre-wrap" }}
        >
          {v}
        </Typography.Paragraph>
      ),
    },
    {
      title: "Status",
      dataIndex: "status",
      key: "status",
      width: 100,
      render: (v: number) => {
        const s = statusMap[v] ?? { label: `Unknown(${v})`, color: "default" };
        return <Tag color={s.color}>{s.label}</Tag>;
      },
    },
    {
      title: "Audience",
      dataIndex: "audience_type",
      key: "audience_type",
      width: 110,
      render: (v: string) => <Tag>{v}</Tag>,
    },
    {
      title: "Start At",
      dataIndex: "start_at",
      key: "start_at",
      width: 180,
      render: (v: number) => formatTimestamp(v),
    },
    {
      title: "End At",
      dataIndex: "end_at",
      key: "end_at",
      width: 180,
      render: (v: number) => formatTimestamp(v),
    },
    {
      title: "Offline At",
      dataIndex: "offline_at",
      key: "offline_at",
      width: 180,
      render: (v: number) => formatTimestamp(v),
    },
    {
      title: "Created At",
      dataIndex: "created_at",
      key: "created_at",
      width: 180,
      render: (v: number) => formatTimestamp(v),
    },
    {
      title: "Actions",
      key: "actions",
      width: 160,
      fixed: "right",
      render: (_, record) => (
        <Space>
          <Button size="small" onClick={() => openEditModal(record)}>
            Edit
          </Button>
          {record.status !== 2 && (
            <Button size="small" danger onClick={() => void handleOffline(record)}>
              Offline
            </Button>
          )}
        </Space>
      ),
    },
  ];

  return (
    <>
      {contextHolder}
      <List
        headerButtons={
          <Space wrap>
            <Button
              type="primary"
              onClick={() => {
                createForm.setFieldsValue({
                  type: "announcement",
                  content: "",
                  status: 1,
                });
                setCreateOpen(true);
              }}
            >
              New Notification
            </Button>
            <Select
              allowClear
              placeholder="Filter by status"
              style={{ width: 160 }}
              value={statusFilter}
              options={[
                { label: "Draft", value: 0 },
                { label: "Active", value: 1 },
                { label: "Offline", value: 2 },
              ]}
              onChange={(value) => {
                setStatusFilter(value);
                setCurrent(1);
              }}
            />
          </Space>
        }
      >
        <Table
          dataSource={query.data?.data}
          columns={columns}
          rowKey="notification_id"
          loading={query.isLoading}
          scroll={{ x: 1800 }}
          pagination={{
            current,
            pageSize,
            total: query.data?.total ?? 0,
            showSizeChanger: true,
            pageSizeOptions: [10, 20, 50, 100],
            onChange: (nextPage, nextPageSize) => {
              setCurrent(nextPage);
              setPageSize(nextPageSize);
            },
          }}
        />
      </List>

      {/* Create Modal */}
      <Modal
        title="Create System Notification"
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={() => void handleCreate()}
        okButtonProps={{ loading: submitting }}
        destroyOnHidden
      >
        <Form form={createForm} layout="vertical">
          <Form.Item name="type" label="Type" rules={[{ required: true }]}>
            <Select
              options={[
                { label: "Announcement (one-time delivery)", value: "announcement" },
                { label: "System (persistent while active)", value: "system" },
              ]}
            />
          </Form.Item>
          <Form.Item name="content" label="Content" rules={[{ required: true }]}>
            <Input.TextArea rows={4} />
          </Form.Item>
          <Form.Item name="status" label="Status" rules={[{ required: true }]}>
            <Select
              options={[
                { label: "Draft", value: 0 },
                { label: "Active", value: 1 },
              ]}
            />
          </Form.Item>
          <Form.Item name="time_range" label="Effective Window (optional)">
            <DatePicker.RangePicker showTime style={{ width: "100%" }} />
          </Form.Item>
        </Form>
      </Modal>

      {/* Edit Modal */}
      <Modal
        title={editingNotif ? `Edit Notification #${editingNotif.notification_id}` : "Edit"}
        open={editOpen}
        onCancel={() => {
          setEditOpen(false);
          setEditingNotif(null);
        }}
        onOk={() => void handleEdit()}
        okButtonProps={{ loading: submitting }}
        destroyOnHidden
      >
        <Form form={editForm} layout="vertical">
          <Form.Item name="type" label="Type" rules={[{ required: true }]}>
            <Select
              options={[
                { label: "Announcement (one-time delivery)", value: "announcement" },
                { label: "System (persistent while active)", value: "system" },
              ]}
            />
          </Form.Item>
          <Form.Item name="content" label="Content" rules={[{ required: true }]}>
            <Input.TextArea rows={4} />
          </Form.Item>
          <Form.Item name="status" label="Status" rules={[{ required: true }]}>
            <Select
              options={[
                { label: "Draft", value: 0 },
                { label: "Active", value: 1 },
              ]}
            />
          </Form.Item>
          <Form.Item name="time_range" label="Effective Window (optional)">
            <DatePicker.RangePicker showTime style={{ width: "100%" }} allowEmpty />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
