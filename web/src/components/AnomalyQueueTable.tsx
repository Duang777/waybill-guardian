import {
  type Column,
  type ColumnDef,
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  type SortingState,
  useReactTable,
} from "@tanstack/react-table";
import { ArrowUpDown, ChevronDown, ChevronRight } from "lucide-react";
import { useMemo, useState } from "react";
import type { WaybillID } from "../api";
import { AnimatedNumber } from "./ui/animated-number";
import { Badge, type BadgeTone } from "./ui/badge";
import { ButtonLink } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Tooltip } from "./ui/tooltip";
import styles from "./anomaly-queue-table.module.css";

export type AnomalyQueueRow = {
  waybillID: WaybillID;
  origin: string;
  destination: string;
  anomalyLabel: string;
  rank: number;
  riskScore: number;
  statusLabel: string;
  statusTone: BadgeTone;
  href: string;
  selectable: boolean;
};

type AnomalyQueueTableProps = {
  rows: readonly AnomalyQueueRow[];
  selected: ReadonlySet<WaybillID>;
  selectionDisabled: boolean;
  onToggleSelection: (waybillID: WaybillID) => void;
  className?: string;
};

export function AnomalyQueueTable({
  rows,
  selected,
  selectionDisabled,
  onToggleSelection,
  className,
}: AnomalyQueueTableProps) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const data = useMemo(() => [...rows], [rows]);
  const columns = useMemo<ColumnDef<AnomalyQueueRow>[]>(
    () => [
      {
        id: "selection",
        header: () => <span className={styles.srOnly}>选择</span>,
        enableSorting: false,
        cell: ({ row }) => (
          <QueueCheckbox
            row={row.original}
            checked={selected.has(row.original.waybillID)}
            disabled={selectionDisabled || !row.original.selectable}
            onToggle={onToggleSelection}
          />
        ),
      },
      {
        accessorKey: "rank",
        header: "序",
        enableSorting: false,
        cell: ({ getValue }) => (
          <span className={styles.rank}>
            {String(getValue<number>()).padStart(2, "0")}
          </span>
        ),
      },
      {
        id: "waybill",
        accessorFn: (row) => row.waybillID,
        header: ({ column }) => (
          <SortHeader column={column} label="运单 / 线路" />
        ),
        cell: ({ row }) => <QueueIdentity row={row.original} />,
      },
      {
        accessorKey: "riskScore",
        header: ({ column }) => (
          <SortHeader column={column} label="风险" />
        ),
        cell: ({ getValue }) => (
          <span className={styles.riskScore}>
            <AnimatedNumber
              value={getValue<number>()}
              label={`风险分 ${getValue<number>()}`}
            />
          </span>
        ),
      },
      {
        accessorKey: "statusLabel",
        header: "状态",
        enableSorting: false,
        cell: ({ row }) => (
          <Badge tone={row.original.statusTone}>
            {row.original.statusLabel}
          </Badge>
        ),
      },
      {
        id: "action",
        header: () => <span className={styles.srOnly}>操作</span>,
        enableSorting: false,
        cell: ({ row }) => <QueueLink row={row.original} />,
      },
    ],
    [onToggleSelection, selected, selectionDisabled],
  );
  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    getRowId: (row) => row.waybillID,
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  return (
    <div
      className={[styles.root, className].filter(Boolean).join(" ")}
      data-queue-table
    >
      <table className={styles.table} aria-label="异常处置队列数据">
        <thead>
          {table.getHeaderGroups().map((headerGroup) => (
            <tr key={headerGroup.id}>
              {headerGroup.headers.map((header) => (
                <th
                  aria-sort={sortDirection(header.column.getIsSorted())}
                  key={header.id}
                  scope="col"
                >
                  {header.isPlaceholder
                    ? null
                    : flexRender(
                        header.column.columnDef.header,
                        header.getContext(),
                      )}
                </th>
              ))}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.map((row) => (
            <tr
              data-queue-row
              data-selected={selected.has(row.original.waybillID)}
              data-waybill-id={row.original.waybillID}
              key={row.id}
            >
              {row.getVisibleCells().map((cell) => (
                <td key={cell.id}>
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>

      <div className={styles.mobileList} aria-label="异常处置队列">
        {table.getRowModel().rows.map((row) => (
          <article
            className={styles.mobileRow}
            data-queue-card
            data-selected={selected.has(row.original.waybillID)}
            data-waybill-id={row.original.waybillID}
            key={row.id}
          >
            <QueueCheckbox
              row={row.original}
              checked={selected.has(row.original.waybillID)}
              disabled={selectionDisabled || !row.original.selectable}
              onToggle={onToggleSelection}
            />
            <span className={styles.rank}>
              {String(row.original.rank).padStart(2, "0")}
            </span>
            <QueueIdentity row={row.original} />
            <span className={styles.riskScore}>
              <AnimatedNumber
                value={row.original.riskScore}
                label={`风险分 ${row.original.riskScore}`}
              />
            </span>
            <Badge tone={row.original.statusTone}>
              {row.original.statusLabel}
            </Badge>
            <QueueLink row={row.original} />
          </article>
        ))}
      </div>
    </div>
  );
}

function QueueCheckbox({
  row,
  checked,
  disabled,
  onToggle,
}: {
  row: AnomalyQueueRow;
  checked: boolean;
  disabled: boolean;
  onToggle: (waybillID: WaybillID) => void;
}) {
  return (
    <span className={styles.checkbox}>
      <Checkbox
        aria-label={`选择 ${row.waybillID}`}
        checked={checked}
        disabled={disabled}
        onCheckedChange={() => onToggle(row.waybillID)}
      />
    </span>
  );
}

function QueueIdentity({ row }: { row: AnomalyQueueRow }) {
  return (
    <div className={styles.identity}>
      <a href={row.href}>
        {row.origin} → {row.destination}
      </a>
      <span>
        <span>{row.waybillID}</span>
        <span>{row.anomalyLabel}</span>
      </span>
    </div>
  );
}

function QueueLink({ row }: { row: AnomalyQueueRow }) {
  return (
    <Tooltip content="查看运单" side="left">
      <ButtonLink
        className={styles.drilldown}
        href={row.href}
        variant="icon"
        size="icon"
        aria-label={`查看运单 ${row.waybillID}`}
      >
        <ChevronRight aria-hidden="true" size={17} />
      </ButtonLink>
    </Tooltip>
  );
}

function SortHeader({
  column,
  label,
}: {
  column: Column<AnomalyQueueRow, unknown>;
  label: string;
}) {
  const direction = column.getIsSorted();
  return (
    <button
      className={styles.sortButton}
      type="button"
      onClick={column.getToggleSortingHandler()}
    >
      <span>{label}</span>
      {direction === false ? (
        <ArrowUpDown aria-hidden="true" size={12} />
      ) : (
        <ChevronDown
          aria-hidden="true"
          data-direction={direction}
          size={13}
        />
      )}
    </button>
  );
}

function sortDirection(
  value: false | "asc" | "desc",
): "ascending" | "descending" | "none" {
  switch (value) {
    case "asc":
      return "ascending";
    case "desc":
      return "descending";
    case false:
      return "none";
    default: {
      const exhaustive: never = value;
      return exhaustive;
    }
  }
}
