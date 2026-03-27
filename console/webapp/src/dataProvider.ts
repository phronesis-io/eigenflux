import type { DataProvider } from "@refinedev/core";
import axios from "axios";
import type { AxiosInstance } from "axios";

const axiosInstance = axios.create();

export const consoleDataProvider = (
  apiUrl: string,
  httpClient: AxiosInstance = axiosInstance
): DataProvider => ({
  getList: async ({ resource, pagination, filters }) => {
    const url = `${apiUrl}/${resource}`;

    const { currentPage = 1, pageSize = 10 } = pagination ?? {};

    const query: Record<string, any> = {
      page: currentPage,
      page_size: pageSize,
    };

    // Handle filters
    filters?.forEach((filter) => {
      if ("field" in filter && (filter.operator === "eq" || filter.operator === "contains")) {
        query[filter.field] = filter.value;
      }
    });

    const { data } = await httpClient.get(url, { params: query });

    // Check for API error response
    if (data.code !== 0 || !data.data) {
      throw new Error(data.msg || "API request failed");
    }

    // Transform response to Refine format
    // API response format: { code, msg, data: { agents/items, total, page, page_size } }
    let resourceData: any[] = [];
    if (resource === "agents") {
      resourceData = data.data.agents ?? [];
    } else if (resource === "items") {
      resourceData = data.data.items ?? [];
    } else if (resource === "milestone-rules") {
      resourceData = data.data.rules ?? [];
    } else if (resource === "system-notifications") {
      resourceData = data.data.notifications ?? [];
    }

    return {
      data: resourceData || [],
      total: data.data.total || 0,
    };
  },

  getOne: async ({ resource, id }) => {
    const url = `${apiUrl}/${resource}/${id}`;
    const { data } = await httpClient.get(url);
    if (data.code !== 0 || !data.data) {
      throw new Error(data.msg || "API request failed");
    }
    const inner = data.data;
    const singular: Record<string, string> = {
      agents: "agent",
      items: "item",
      "milestone-rules": "rule",
      "system-notifications": "notification",
    };
    const key = singular[resource];
    return { data: key && inner[key] ? inner[key] : inner };
  },

  create: async ({ resource, variables }) => {
    const url = `${apiUrl}/${resource}`;
    const { data } = await httpClient.post(url, variables);
    return { data };
  },

  update: async ({ resource, id, variables }) => {
    const url = `${apiUrl}/${resource}/${id}`;
    const { data } = await httpClient.put(url, variables);
    if (data.code !== 0) {
      throw new Error(data.msg || "Update failed");
    }
    return { data: data.data };
  },

  deleteOne: async ({ resource, id }) => {
    const url = `${apiUrl}/${resource}/${id}`;
    const { data } = await httpClient.delete(url);
    return { data };
  },

  getApiUrl: () => apiUrl,
});
