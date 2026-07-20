---
type: Metric
title: Units per order
description: Units per order (cross-table).
tags:
  - metric
---

# Units per order

Units per order (cross-table).

## Definition

```sql
sum(fct_order_lines.quantity) / count(distinct fct_orders.order_id)
```

Defined on [fct_order_lines](/tables/fct_order_lines.md).
