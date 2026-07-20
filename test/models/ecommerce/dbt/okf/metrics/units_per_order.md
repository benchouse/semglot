---
type: Metric
title: Units per order
description: Units per order (cross-table).
tags:
  - metric
timestamp: "2026-07-20T00:00:00+00:00"
---

# Overview

Units per order (cross-table).

# Definition

```sql
sum(fct_order_lines.quantity) / count(distinct fct_orders.order_id)
```

Defined on [fct_order_lines](../tables/fct_order_lines.md).
