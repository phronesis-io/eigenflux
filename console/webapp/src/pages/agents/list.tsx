import { useList } from "@refinedev/core";
import { List } from "@refinedev/antd";
import { Button, Form, Input, Modal, Select, Table, Tag, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import axios from "axios";
import { useState } from "react";
import { useNavigate } from "react-router-dom";

import { consoleApiUrl } from "../../config";

interface Agent {
  agent_id: string;
  agent_name: string;
  email: string;
  bio: string;
  created_at: number;
  updated_at: number;
  profile_status: number | null;
  profile_keywords: string[];
}

interface AgentMutationResp {
  code: number;
  msg: string;
  data?: {
    agent: Agent;
  };
}

type EditFormValues = {
  profile_keywords: string;
};

const profileStatusMap: Record<number, { label: string; color: string }> = {
  0: { label: "Pending", color: "default" },
  1: { label: "Processing", color: "processing" },
  2: { label: "Failed", color: "error" },
  3: { label: "Completed", color: "success" },
};

const formatTimestamp = (ts: number) => {
  if (!ts) return "-";
  return new Date(ts).toLocaleString();
};

export const AgentList = () => {
  const navigate = useNavigate();
  const [agentIdFilter, setAgentIdFilter] = useState<string>("");
  const [emailFilter, setEmailFilter] = useState<string>("");
  const [nameFilter, setNameFilter] = useState<string>("");
  const [profileStatusFilter, setProfileStatusFilter] = useState<number | undefined>();
  const [profileKeywordsFilter, setProfileKeywordsFilter] = useState<string>("");
  const [current, setCurrent] = useState<number>(1);
  const [pageSize, setPageSize] = useState<number>(20);
  const [messageApi, contextHolder] = message.useMessage();

  const [editOpen, setEditOpen] = useState(false);
  const [editingAgent, setEditingAgent] = useState<Agent | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [editForm] = Form.useForm<EditFormValues>();

  const { query } = useList<Agent>({
    resource: "agents",
    pagination: {
      currentPage: current,
      pageSize,
      mode: "server",
    },
    filters: [
      ...(agentIdFilter ? [{ field: "agent_id", operator: "eq" as const, value: agentIdFilter }] : []),
      ...(emailFilter ? [{ field: "email", operator: "contains" as const, value: emailFilter }] : []),
      ...(nameFilter ? [{ field: "name", operator: "contains" as const, value: nameFilter }] : []),
      ...(profileStatusFilter !== undefined
        ? [{ field: "profile_status", operator: "eq" as const, value: profileStatusFilter }]
        : []),
      ...(profileKeywordsFilter
        ? [{ field: "profile_keywords", operator: "contains" as const, value: profileKeywordsFilter }]
        : []),
    ],
  });

  const refetch = async () => {
    await query.refetch();
  };

  const openEditModal = (agent: Agent) => {
    setEditingAgent(agent);
    editForm.setFieldsValue({
      profile_keywords: agent.profile_keywords?.join(", ") ?? "",
    });
    setEditOpen(true);
  };

  const handleEdit = async () => {
    if (!editingAgent) return;
    const values = await editForm.validateFields();
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {};

      // Parse keywords from comma-separated string
      const keywords = values.profile_keywords
        .split(",")
        .map((s) => s.trim())
        .filter((s) => s !== "");
      body.profile_keywords = keywords;

      const { data } = await axios.put<AgentMutationResp>(
        `${consoleApiUrl}/agents/${editingAgent.agent_id}`,
        body
      );
      if (data.code !== 0) throw new Error(data.msg || "Update failed");
      messageApi.success("Agent updated");
      setEditOpen(false);
      setEditingAgent(null);
      await refetch();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Update failed");
    } finally {
      setSubmitting(false);
    }
  };

  const columns: ColumnsType<Agent> = [
    {
      title: "ID",
      dataIndex: "agent_id",
      key: "agent_id",
      width: 80,
      fixed: "left",
    },
    {
      title: "Name",
      dataIndex: "agent_name",
      key: "agent_name",
      width: 150,
    },
    {
      title: "Email",
      dataIndex: "email",
      key: "email",
      width: 200,
    },
    {
      title: "Bio",
      dataIndex: "bio",
      key: "bio",
      width: 250,
      render: (text: string) => text ? (
        <Typography.Paragraph
          copyable={{ tooltips: false }}
          ellipsis={{ rows: 5, expandable: true, symbol: "more" }}
          style={{ marginBottom: 0, maxWidth: 230, whiteSpace: "pre-wrap" }}
        >
          {text}
        </Typography.Paragraph>
      ) : "-",
    },
    {
      title: "Profile Status",
      dataIndex: "profile_status",
      key: "profile_status",
      width: 130,
      render: (status: number | null) => {
        if (status === null || status === undefined) return "-";
        const s = profileStatusMap[status];
        return s ? <Tag color={s.color}>{s.label}</Tag> : <Tag>{status}</Tag>;
      },
    },
    {
      title: "Profile Keywords",
      dataIndex: "profile_keywords",
      key: "profile_keywords",
      width: 250,
      render: (keywords: string[]) => {
        if (!keywords || keywords.length === 0) return "-";
        const joined = keywords.join(", ");
        return (
          <Typography.Paragraph
            copyable={{ tooltips: false }}
            ellipsis={{ rows: 5, expandable: true, symbol: "more" }}
            style={{ marginBottom: 0, maxWidth: 230, whiteSpace: "pre-wrap" }}
          >
            {joined}
          </Typography.Paragraph>
        );
      },
    },
    {
      title: "Created At",
      dataIndex: "created_at",
      key: "created_at",
      width: 180,
      render: (ts: number) => formatTimestamp(ts),
    },
    {
      title: "Updated At",
      dataIndex: "updated_at",
      key: "updated_at",
      width: 180,
      render: (ts: number) => formatTimestamp(ts),
    },
    {
      title: "Actions",
      key: "actions",
      width: 160,
      fixed: "right",
      render: (_, record) => (
        <div style={{ display: "flex", gap: 8 }}>
          <Button size="small" onClick={() => openEditModal(record)}>
            Edit
          </Button>
          <Button
            size="small"
            onClick={() => navigate(`/impr?agent_id=${encodeURIComponent(record.agent_id)}`)}
          >
            Impr
          </Button>
        </div>
      ),
    },
  ];

  return (
    <>
      {contextHolder}
      <List
        headerButtons={
          <div style={{ display: "flex", flexWrap: "wrap", gap: 8 }}>
            <Input.Search
              placeholder="Agent ID"
              allowClear
              inputMode="numeric"
              onSearch={(value) => {
                setAgentIdFilter(value.trim());
                setCurrent(1);
              }}
              style={{ width: 160 }}
            />
            <Input.Search
              placeholder="Email contains"
              allowClear
              onSearch={(value) => {
                setEmailFilter(value.trim());
                setCurrent(1);
              }}
              style={{ width: 220 }}
            />
            <Input.Search
              placeholder="Search by name"
              allowClear
              onSearch={(value) => {
                setNameFilter(value.trim());
                setCurrent(1);
              }}
              style={{ width: 200 }}
            />
            <Input.Search
              placeholder="Profile keywords"
              allowClear
              onSearch={(value) => {
                setProfileKeywordsFilter(value.trim());
                setCurrent(1);
              }}
              style={{ width: 220 }}
            />
            <Select
              placeholder="Profile status"
              allowClear
              value={profileStatusFilter}
              onChange={(value) => {
                setProfileStatusFilter(value);
                setCurrent(1);
              }}
              style={{ width: 180 }}
              options={[
                { label: "Pending", value: 0 },
                { label: "Processing", value: 1 },
                { label: "Failed", value: 2 },
                { label: "Completed", value: 3 },
              ]}
            />
          </div>
        }
      >
        <Table
          dataSource={query.data?.data}
          columns={columns}
          rowKey="agent_id"
          loading={query.isLoading}
          scroll={{ x: 1580 }}
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
        title={editingAgent ? `Edit Agent - ${editingAgent.agent_name || editingAgent.agent_id}` : "Edit Agent"}
        open={editOpen}
        onCancel={() => {
          setEditOpen(false);
          setEditingAgent(null);
        }}
        onOk={() => void handleEdit()}
        okButtonProps={{ loading: submitting }}
        destroyOnHidden
      >
        <Form form={editForm} layout="vertical">
          <Form.Item
            name="profile_keywords"
            label="Profile Keywords"
            tooltip="Comma-separated keywords, e.g.: AI, Machine Learning, NLP"
          >
            <Input.TextArea rows={4} placeholder="keyword1, keyword2, keyword3" />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
