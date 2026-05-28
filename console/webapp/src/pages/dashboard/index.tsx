import { useState, useEffect, useCallback } from "react";
import {
  Card,
  Row,
  Col,
  Statistic,
  Button,
  Table,
  Spin,
  message,
  Tag,
  Typography,
} from "antd";
import { ReloadOutlined, ArrowUpOutlined, ArrowDownOutlined } from "@ant-design/icons";
import { Line, Bar, Pie, Column } from "@ant-design/charts";
import axios from "axios";
import { consoleApiUrl } from "../../config";

const { Title } = Typography;

interface SnapshotData {
  summary: {
    total_items: number;
    active_items: number;
    total_users: number;
    avg_quality_score: number;
  };
  keyword_analysis: {
    item_keywords: { keyword: string; count: number }[];
    user_keywords: { keyword: string; count: number }[];
    overlap: { keyword: string; item_count: number; user_count: number }[];
    supply_only: { keyword: string; count: number }[];
    demand_only: { keyword: string; count: number }[];
  };
  domain_analysis: {
    broadcast_type_distribution: Record<string, number>;
    top_domains: { domain: string; count: number; avg_consumed: number }[];
  };
  engagement: {
    consumed_rate_by_keyword: { keyword: string; rate: number }[];
    quality_distribution: { range: string; count: number }[];
    top50_items: {
      item_id: number;
      keywords: string;
      consumed_count: number;
      total_score: number;
      quality_score: number;
    }[];
  };
}

interface TrendPoint {
  snapshot_id: number;
  created_at: number;
  total_items: number;
  active_items: number;
  total_users: number;
  avg_quality_score: number;
  overlap_count: number;
  supply_only_count: number;
  demand_only_count: number;
}

interface Snapshot {
  snapshot_id: number;
  snapshot_type: string;
  data: SnapshotData;
  created_at: number;
}

export const DashboardPage: React.FC = () => {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [trends, setTrends] = useState<TrendPoint[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [messageApi, contextHolder] = message.useMessage();

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const [latestRes, trendsRes] = await Promise.all([
        axios.get(`${consoleApiUrl}/dashboard/snapshots/latest`),
        axios.get(`${consoleApiUrl}/dashboard/trends`),
      ]);
      if (latestRes.data.code === 0 && latestRes.data.data?.snapshot) {
        setSnapshot(latestRes.data.data.snapshot);
      }
      if (trendsRes.data.code === 0 && trendsRes.data.data?.trends) {
        setTrends(trendsRes.data.data.trends);
      }
    } catch {
      messageApi.warning("No dashboard data yet. Click Refresh to generate the first snapshot.");
    } finally {
      setLoading(false);
    }
  }, [messageApi]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const handleRefresh = async () => {
    setRefreshing(true);
    try {
      const res = await axios.post(`${consoleApiUrl}/dashboard/snapshots/refresh`);
      if (res.data.code !== 0) throw new Error(res.data.msg);
      messageApi.success("Snapshot refreshed");
      await fetchData();
    } catch (err: any) {
      messageApi.error(err.message || "Refresh failed");
    } finally {
      setRefreshing(false);
    }
  };

  const data = snapshot?.data;
  const prevTrend = trends.length >= 2 ? trends[trends.length - 2] : null;

  const formatDate = (ms: number) =>
    new Date(ms).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });

  if (loading) {
    return (
      <div style={{ textAlign: "center", padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }

  return (
    <div style={{ padding: 24 }}>
      {contextHolder}

      {/* Header */}
      <Row justify="space-between" align="middle" style={{ marginBottom: 24 }}>
        <Col>
          <Title level={3} style={{ margin: 0 }}>
            Content Market Fit Dashboard
          </Title>
          {snapshot && (
            <span style={{ color: "#888" }}>
              Last updated: {formatDate(snapshot.created_at)}
            </span>
          )}
        </Col>
        <Col>
          <Button
            type="primary"
            icon={<ReloadOutlined />}
            loading={refreshing}
            onClick={handleRefresh}
          >
            Refresh
          </Button>
        </Col>
      </Row>

      {!data ? (
        <Card>
          <p>No snapshot data available. Click Refresh to generate the first snapshot.</p>
        </Card>
      ) : (
        <>
          {/* Summary Cards */}
          <Row gutter={16} style={{ marginBottom: 24 }}>
            <Col span={6}>
              <Card>
                <Statistic
                  title="Total Items"
                  value={data.summary.total_items}
                  suffix={
                    prevTrend && (
                      <DeltaTag
                        current={data.summary.total_items}
                        previous={prevTrend.total_items}
                      />
                    )
                  }
                />
              </Card>
            </Col>
            <Col span={6}>
              <Card>
                <Statistic
                  title="Active Items"
                  value={data.summary.active_items}
                  suffix={
                    prevTrend && (
                      <DeltaTag
                        current={data.summary.active_items}
                        previous={prevTrend.active_items}
                      />
                    )
                  }
                />
              </Card>
            </Col>
            <Col span={6}>
              <Card>
                <Statistic
                  title="Users (completed profile)"
                  value={data.summary.total_users}
                  suffix={
                    prevTrend && (
                      <DeltaTag
                        current={data.summary.total_users}
                        previous={prevTrend.total_users}
                      />
                    )
                  }
                />
              </Card>
            </Col>
            <Col span={6}>
              <Card>
                <Statistic
                  title="Avg Quality Score"
                  value={data.summary.avg_quality_score}
                  precision={2}
                  suffix={
                    prevTrend && (
                      <DeltaTag
                        current={data.summary.avg_quality_score}
                        previous={prevTrend.avg_quality_score}
                        precision={2}
                      />
                    )
                  }
                />
              </Card>
            </Col>
          </Row>

          {/* Trend Charts */}
          {trends.length > 1 && (
            <Card title="Trends Over Time" style={{ marginBottom: 24 }}>
              <Row gutter={16}>
                <Col span={12}>
                  <Line
                    data={trends.flatMap((t) => [
                      { date: formatDate(t.created_at), metric: "Total Items", value: t.total_items },
                      { date: formatDate(t.created_at), metric: "Active Items", value: t.active_items },
                      { date: formatDate(t.created_at), metric: "Users", value: t.total_users },
                    ])}
                    xField="date"
                    yField="value"
                    colorField="metric"
                    height={300}
                  />
                </Col>
                <Col span={12}>
                  <Line
                    data={trends.map((t) => ({
                      date: formatDate(t.created_at),
                      value: t.avg_quality_score,
                    }))}
                    xField="date"
                    yField="value"
                    height={300}
                    axis={{ y: { title: "Avg Quality Score" } }}
                  />
                </Col>
              </Row>
            </Card>
          )}

          {/* Keyword Supply vs Demand */}
          <Card title="Keyword Supply vs Demand" style={{ marginBottom: 24 }}>
            <Row gutter={16}>
              <Col span={12}>
                <Title level={5}>Top Item Keywords (Supply)</Title>
                <Bar
                  data={data.keyword_analysis.item_keywords.slice(0, 20)}
                  xField="count"
                  yField="keyword"
                  height={400}
                  axis={{ y: { title: "" } }}
                />
              </Col>
              <Col span={12}>
                <Title level={5}>Top User Keywords (Demand)</Title>
                <Bar
                  data={data.keyword_analysis.user_keywords.slice(0, 20)}
                  xField="count"
                  yField="keyword"
                  height={400}
                  axis={{ y: { title: "" } }}
                />
              </Col>
            </Row>
            <Row gutter={16} style={{ marginTop: 24 }}>
              <Col span={8}>
                <Title level={5}>
                  Overlap <Tag color="green">{data.keyword_analysis.overlap.length}</Tag>
                </Title>
                <Table
                  dataSource={data.keyword_analysis.overlap}
                  rowKey="keyword"
                  size="small"
                  pagination={{ pageSize: 10 }}
                  columns={[
                    { title: "Keyword", dataIndex: "keyword", key: "keyword" },
                    { title: "Items", dataIndex: "item_count", key: "item_count", sorter: (a: any, b: any) => a.item_count - b.item_count },
                    { title: "Users", dataIndex: "user_count", key: "user_count", sorter: (a: any, b: any) => a.user_count - b.user_count },
                  ]}
                />
              </Col>
              <Col span={8}>
                <Title level={5}>
                  Supply Only <Tag color="orange">{data.keyword_analysis.supply_only.length}</Tag>
                </Title>
                <Table
                  dataSource={data.keyword_analysis.supply_only}
                  rowKey="keyword"
                  size="small"
                  pagination={{ pageSize: 10 }}
                  columns={[
                    { title: "Keyword", dataIndex: "keyword", key: "keyword" },
                    { title: "Items", dataIndex: "count", key: "count", sorter: (a: any, b: any) => a.count - b.count },
                  ]}
                />
              </Col>
              <Col span={8}>
                <Title level={5}>
                  Demand Only <Tag color="red">{data.keyword_analysis.demand_only.length}</Tag>
                </Title>
                <Table
                  dataSource={data.keyword_analysis.demand_only}
                  rowKey="keyword"
                  size="small"
                  pagination={{ pageSize: 10 }}
                  columns={[
                    { title: "Keyword", dataIndex: "keyword", key: "keyword" },
                    { title: "Users", dataIndex: "count", key: "count", sorter: (a: any, b: any) => a.count - b.count },
                  ]}
                />
              </Col>
            </Row>
          </Card>

          {/* Domain Distribution */}
          <Card title="Domain Distribution" style={{ marginBottom: 24 }}>
            <Row gutter={16}>
              <Col span={8}>
                <Title level={5}>Broadcast Type</Title>
                <Pie
                  data={Object.entries(data.domain_analysis.broadcast_type_distribution).map(
                    ([type_, count]) => ({ type: type_, count })
                  )}
                  angleField="count"
                  colorField="type"
                  height={300}
                  label={{ text: "type" }}
                />
              </Col>
              <Col span={16}>
                <Title level={5}>Top Domains (by item count, with avg consumed)</Title>
                <Column
                  data={data.domain_analysis.top_domains.slice(0, 20)}
                  xField="domain"
                  yField="count"
                  height={300}
                  label={{ text: (d: any) => `${d.count} (avg: ${d.avg_consumed})` }}
                />
              </Col>
            </Row>
          </Card>

          {/* Content Freshness & Engagement */}
          <Card title="Content Freshness & Engagement" style={{ marginBottom: 24 }}>
            <Row gutter={16}>
              <Col span={12}>
                <Title level={5}>Quality Score Distribution</Title>
                <Column
                  data={data.engagement.quality_distribution}
                  xField="range"
                  yField="count"
                  height={300}
                />
              </Col>
              <Col span={12}>
                <Title level={5}>Avg Consumed by Keyword (Top 20)</Title>
                <Bar
                  data={data.engagement.consumed_rate_by_keyword}
                  xField="rate"
                  yField="keyword"
                  height={400}
                  axis={{ y: { title: "" } }}
                />
              </Col>
            </Row>

            <Title level={5} style={{ marginTop: 24 }}>
              Top 50 Items by Engagement
            </Title>
            <Table
              dataSource={data.engagement.top50_items}
              rowKey="item_id"
              size="small"
              pagination={{ pageSize: 20 }}
              columns={[
                {
                  title: "Item ID",
                  dataIndex: "item_id",
                  key: "item_id",
                  render: (v: number) => String(v),
                },
                { title: "Keywords", dataIndex: "keywords", key: "keywords", ellipsis: true, width: 300 },
                {
                  title: "Consumed",
                  dataIndex: "consumed_count",
                  key: "consumed_count",
                  sorter: (a: any, b: any) => a.consumed_count - b.consumed_count,
                },
                {
                  title: "Total Score",
                  dataIndex: "total_score",
                  key: "total_score",
                  sorter: (a: any, b: any) => a.total_score - b.total_score,
                },
                {
                  title: "Quality",
                  dataIndex: "quality_score",
                  key: "quality_score",
                  render: (v: number) => v?.toFixed(2) ?? "N/A",
                  sorter: (a: any, b: any) => (a.quality_score ?? 0) - (b.quality_score ?? 0),
                },
              ]}
            />
          </Card>
        </>
      )}
    </div>
  );
};

const DeltaTag: React.FC<{
  current: number;
  previous: number;
  precision?: number;
}> = ({ current, previous, precision = 0 }) => {
  const delta = current - previous;
  if (delta === 0) return null;
  const formatted = precision > 0 ? Math.abs(delta).toFixed(precision) : Math.abs(delta);
  return delta > 0 ? (
    <span style={{ fontSize: 14, color: "#3f8600", marginLeft: 4 }}>
      <ArrowUpOutlined /> {formatted}
    </span>
  ) : (
    <span style={{ fontSize: 14, color: "#cf1322", marginLeft: 4 }}>
      <ArrowDownOutlined /> {formatted}
    </span>
  );
};
