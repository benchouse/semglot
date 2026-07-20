---
type: Metric
title: Refunded orders
description: Count of refunded orders.
tags:
  - metric
timestamp: "2026-07-20T00:00:00+00:00"
---

# Overview

Count of refunded orders.

# Definition

```sql
sum(case when fct_orders.is_refunded then 1 else 0 end)
```

Defined on [fct_orders](../tables/fct_orders.md).
