import { useList } from "@refinedev/core";
import { List } from "@refinedev/antd";
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import axios from "axios";
import { useState } from "react";

import { consoleApiUrl } from "../../config";
import { useSessionState } from "../../hooks/useSessionState";

interface MilestoneRule {
  rule_id: string;
  metric_key: string;
  threshold: number;
  rule_enabled: boolean;
  content_template: string;
  created_at: number;
  updated_at: number;
}

interface RuleMutationResp {
  code: number;
  msg: string;
  data?: {
    rule?: MilestoneRule;
    old_rule?: MilestoneRule;
    new_rule?: MilestoneRule;
  };
}

type CreateFormValues = {
  metric_key: string;
  threshold: number;
  rule_enabled: boolean;
  content_template: string;
};

type EditFormValues = {
  rule_enabled: boolean;
  content_template: string;
};

type ReplaceFormValues = {
  metric_key: string;
  threshold: number;
  rule_enabled: boolean;
  content_template: string;
};

const metricOptions = [
  { label: "consumed", value: "consumed" },
  { label: "score_1", value: "score_1" },
  { label: "score_2", value: "score_2" },
];

const formatTimestamp = (ts: number) => {
  if (!ts) return "-";
  return new Date(ts).toLocaleString();
};

const TemplatePreview = ({ value }: { value: string }) => (
  <Typography.Paragraph
    copyable={{ tooltips: false }}
    ellipsis={{ rows: 2, expandable: true, symbol: "more" }}
    style={{ marginBottom: 0, maxWidth: 520, whiteSpace: "pre-wrap" }}
  >
    {value}
  </Typography.Paragraph>
);

export const MilestoneRuleList = () => {
  const [metricFilter, setMetricFilter] = useSessionState<string | undefined>(
    "milestone-rules.metric",
    undefined,
  );
  const [enabledFilter, setEnabledFilter] = useSessionState<boolean | undefined>(
    "milestone-rules.enabled",
    undefined,
  );
  const [current, setCurrent] = useSessionState("milestone-rules.page", 1);
  const [pageSize, setPageSize] = useSessionState("milestone-rules.page-size", 20);
  const [messageApi, contextHolder] = message.useMessage();

  const [createOpen, setCreateOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [replaceOpen, setReplaceOpen] = useState(false);
  const [editingRule, setEditingRule] = useState<MilestoneRule | null>(null);
  const [replacingRule, setReplacingRule] = useState<MilestoneRule | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const [createForm] = Form.useForm<CreateFormValues>();
  const [editForm] = Form.useForm<EditFormValues>();
  const [replaceForm] = Form.useForm<ReplaceFormValues>();

  const { query } = useList<MilestoneRule>({
    resource: "milestone-rules",
    pagination: {
      currentPage: current,
      pageSize,
      mode: "server",
    },
    filters: [
      ...(metricFilter ? [{ field: "metric_key", operator: "eq" as const, value: metricFilter }] : []),
      ...(enabledFilter !== undefined
        ? [{ field: "rule_enabled", operator: "eq" as const, value: enabledFilter }]
        : []),
    ],
  });

  const refetchRules = async () => {
    await query.refetch();
  };

  const handleCreate = async () => {
    const values = await createForm.validateFields();
    setSubmitting(true);
    try {
      const { data } = await axios.post<RuleMutationResp>(`${consoleApiUrl}/milestone-rules`, values);
      if (data.code !== 0) {
        throw new Error(data.msg || "Create milestone rule failed");
      }
      messageApi.success("Milestone rule created");
      setCreateOpen(false);
      createForm.resetFields();
      await refetchRules();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Create milestone rule failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handleEdit = async () => {
    if (!editingRule) return;
    const values = await editForm.validateFields();
    setSubmitting(true);
    try {
      const { data } = await axios.put<RuleMutationResp>(
        `${consoleApiUrl}/milestone-rules/${editingRule.rule_id}`,
        values
      );
      if (data.code !== 0) {
        throw new Error(data.msg || "Update milestone rule failed");
      }
      messageApi.success("Milestone rule updated");
      setEditOpen(false);
      setEditingRule(null);
      await refetchRules();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Update milestone rule failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handleReplace = async () => {
    if (!replacingRule) return;
    const values = await replaceForm.validateFields();
    setSubmitting(true);
    try {
      const { data } = await axios.post<RuleMutationResp>(
        `${consoleApiUrl}/milestone-rules/${replacingRule.rule_id}/replace`,
        values
      );
      if (data.code !== 0) {
        throw new Error(data.msg || "Replace milestone rule failed");
      }
      messageApi.success("Milestone rule replaced");
      setReplaceOpen(false);
      setReplacingRule(null);
      await refetchRules();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Replace milestone rule failed");
    } finally {
      setSubmitting(false);
    }
  };

  const openEditModal = (rule: MilestoneRule) => {
    setEditingRule(rule);
    editForm.setFieldsValue({
      rule_enabled: rule.rule_enabled,
      content_template: rule.content_template,
    });
    setEditOpen(true);
  };

  const openReplaceModal = (rule: MilestoneRule) => {
    setReplacingRule(rule);
    replaceForm.setFieldsValue({
      metric_key: rule.metric_key,
      threshold: rule.threshold,
      rule_enabled: true,
      content_template: rule.content_template,
    });
    setReplaceOpen(true);
  };

  const columns: ColumnsType<MilestoneRule> = [
    {
      title: "Rule ID",
      dataIndex: "rule_id",
      key: "rule_id",
      width: 120,
      fixed: "left",
    },
    {
      title: "Metric",
      dataIndex: "metric_key",
      key: "metric_key",
      width: 120,
      render: (value: string) => <Tag color="blue">{value}</Tag>,
    },
    {
      title: "Threshold",
      dataIndex: "threshold",
      key: "threshold",
      width: 120,
    },
    {
      title: "Enabled",
      dataIndex: "rule_enabled",
      key: "rule_enabled",
      width: 110,
      render: (enabled: boolean) => (
        <Tag color={enabled ? "success" : "default"}>{enabled ? "Enabled" : "Disabled"}</Tag>
      ),
    },
    {
      title: "Content Template",
      dataIndex: "content_template",
      key: "content_template",
      width: 560,
      render: (value: string) => <TemplatePreview value={value} />,
    },
    {
      title: "Updated At",
      dataIndex: "updated_at",
      key: "updated_at",
      width: 180,
      render: (value: number) => formatTimestamp(value),
    },
    {
      title: "Created At",
      dataIndex: "created_at",
      key: "created_at",
      width: 180,
      render: (value: number) => formatTimestamp(value),
    },
    {
      title: "Actions",
      key: "actions",
      width: 180,
      fixed: "right",
      render: (_, record) => (
        <Space>
          <Button size="small" onClick={() => openEditModal(record)}>
            Edit
          </Button>
          <Button size="small" onClick={() => openReplaceModal(record)}>
            Replace
          </Button>
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
                  metric_key: "consumed",
                  threshold: 50,
                  rule_enabled: true,
                  content_template: 'Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}',
                });
                setCreateOpen(true);
              }}
            >
              New Rule
            </Button>
            <Select
              allowClear
              placeholder="Filter by metric"
              style={{ width: 160 }}
              value={metricFilter}
              options={metricOptions}
              onChange={(value) => {
                setMetricFilter(value);
                setCurrent(1);
              }}
            />
            <Select
              allowClear
              placeholder="Filter by enabled"
              style={{ width: 160 }}
              value={enabledFilter}
              options={[
                { label: "Enabled", value: true },
                { label: "Disabled", value: false },
              ]}
              onChange={(value) => {
                setEnabledFilter(value);
                setCurrent(1);
              }}
            />
          </Space>
        }
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="Changing metric_key or threshold should go through Replace so the old rule is disabled and the new rule gets a fresh rule_id."
        />
        <Table
          dataSource={query.data?.data}
          columns={columns}
          rowKey="rule_id"
          loading={query.isLoading}
          scroll={{ x: 1700 }}
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

      <Modal
        title="Create Milestone Rule"
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={() => void handleCreate()}
        okButtonProps={{ loading: submitting }}
        destroyOnHidden
      >
        <Form form={createForm} layout="vertical">
          <Form.Item name="metric_key" label="Metric" rules={[{ required: true }]}>
            <Select options={metricOptions} />
          </Form.Item>
          <Form.Item
            name="threshold"
            label="Threshold"
            rules={[{ required: true, type: "number", min: 1 }]}
          >
            <InputNumber min={1} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item name="rule_enabled" label="Enabled" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="content_template" label="Content Template" rules={[{ required: true }]}>
            <Input.TextArea rows={5} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={editingRule ? `Edit Rule #${editingRule.rule_id}` : "Edit Rule"}
        open={editOpen}
        onCancel={() => {
          setEditOpen(false);
          setEditingRule(null);
        }}
        onOk={() => void handleEdit()}
        okButtonProps={{ loading: submitting }}
        destroyOnHidden
      >
        <Form form={editForm} layout="vertical">
          <Form.Item name="rule_enabled" label="Enabled" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="content_template" label="Content Template" rules={[{ required: true }]}>
            <Input.TextArea rows={5} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={replacingRule ? `Replace Rule #${replacingRule.rule_id}` : "Replace Rule"}
        open={replaceOpen}
        onCancel={() => {
          setReplaceOpen(false);
          setReplacingRule(null);
        }}
        onOk={() => void handleReplace()}
        okButtonProps={{ loading: submitting }}
        destroyOnHidden
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="Replace will disable the current rule and create a new one."
        />
        <Form form={replaceForm} layout="vertical">
          <Form.Item name="metric_key" label="Metric" rules={[{ required: true }]}>
            <Select options={metricOptions} />
          </Form.Item>
          <Form.Item
            name="threshold"
            label="Threshold"
            rules={[{ required: true, type: "number", min: 1 }]}
          >
            <InputNumber min={1} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item name="rule_enabled" label="New Rule Enabled" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="content_template" label="Content Template" rules={[{ required: true }]}>
            <Input.TextArea rows={5} />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
