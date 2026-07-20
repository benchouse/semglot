---
type: Metric
title: Refund rate
description: Refunded orders / all orders.
tags:
  - metric
timestamp: "2026-07-20T00:00:00+00:00"
---

# Overview

Refunded orders / all orders.

# Definition

```sql
sum(case when fct_orders.is_refunded then 1 else 0 end) / count(distinct fct_orders.order_id)
```

Defined on [fct_orders](../tables/fct_orders.md).
