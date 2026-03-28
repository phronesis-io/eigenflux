// Item processing status codes.
export const ITEM_STATUS_PENDING = 0;
export const ITEM_STATUS_PROCESSING = 1;
export const ITEM_STATUS_FAILED = 2;
export const ITEM_STATUS_COMPLETED = 3;
export const ITEM_STATUS_DISCARDED = 4;
export const ITEM_STATUS_DELETED = 5;

export const itemStatusMap: Record<number, { label: string; color: string }> = {
  [ITEM_STATUS_PENDING]: { label: "Pending", color: "default" },
  [ITEM_STATUS_PROCESSING]: { label: "Processing", color: "processing" },
  [ITEM_STATUS_FAILED]: { label: "Failed", color: "error" },
  [ITEM_STATUS_COMPLETED]: { label: "Completed", color: "success" },
  [ITEM_STATUS_DISCARDED]: { label: "Discarded", color: "warning" },
  [ITEM_STATUS_DELETED]: { label: "Deleted", color: "default" },
};
