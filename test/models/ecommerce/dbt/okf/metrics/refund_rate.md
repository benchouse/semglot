---
type: Metric
title: Refund rate
description: Refunded orders / all orders.
tags:
  - metric
---

# Refund rate

Refunded orders / all orders.

## Definition

```sql
sum(case when fct_orders.is_refunded then 1 else 0 end) / count(distinct fct_orders.order_id)
```

Defined on [fct_orders](/tables/fct_orders.md).
