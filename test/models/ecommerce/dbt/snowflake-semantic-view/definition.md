# ECOMMERCE

This is a Snowflake **semantic view** — use this to understand the intended way to query and aggregate data.

## Definition

```sql
create or replace semantic view ECOMMERCE
	tables (
		FCT_ORDERS as ANALYTICS.MAIN.FCT_ORDERS primary key (ORDER_ID) comment='Order-grain finance fact. One row per order.',
		FCT_ORDER_LINES as ANALYTICS.MAIN.FCT_ORDER_LINES primary key (ORDER_LINE_ID) comment='Order-line grain. One row per line item.',
		DIM_CUSTOMER as ANALYTICS.MAIN.DIM_CUSTOMER primary key (CUSTOMER_SK) comment='Customer dimension.',
		DIM_PRODUCT as ANALYTICS.MAIN.DIM_PRODUCT primary key (PRODUCT_ID) comment='Product dimension.',
		DIM_CHANNEL as ANALYTICS.MAIN.DIM_CHANNEL primary key (CHANNEL_ID) comment='Sales channel dimension.'
	)
	relationships (
		FCT_ORDERS_DIM_CUSTOMER as FCT_ORDERS(CUSTOMER_SK) references DIM_CUSTOMER(CUSTOMER_SK),
		FCT_ORDER_LINES_FCT_ORDERS as FCT_ORDER_LINES(ORDER_ID) references FCT_ORDERS(ORDER_ID),
		FCT_ORDER_LINES_DIM_PRODUCT as FCT_ORDER_LINES(PRODUCT_ID) references DIM_PRODUCT(PRODUCT_ID),
		FCT_ORDERS_DIM_CHANNEL as FCT_ORDERS(CHANNEL_ID) references DIM_CHANNEL(CHANNEL_ID)
	)
	dimensions (
		FCT_ORDERS.ORDER_ID as fct_orders.ORDER_ID comment='Order surrogate key.',
		FCT_ORDERS.CUSTOMER_SK as fct_orders.CUSTOMER_SK comment='Customer the order belongs to.',
		FCT_ORDERS.IS_REFUNDED as fct_orders.IS_REFUNDED comment='Whether the order was refunded.',
		FCT_ORDERS.CHANNEL_ID as fct_orders.CHANNEL_ID comment='Sales channel the order came through.',
		FCT_ORDERS.ORDER_DATE as fct_orders.ORDER_DATE comment='Date the order was placed.',
		FCT_ORDER_LINES.ORDER_LINE_ID as fct_order_lines.ORDER_LINE_ID comment='Line-item surrogate key.',
		FCT_ORDER_LINES.ORDER_ID as fct_order_lines.ORDER_ID comment='Order the line belongs to.',
		FCT_ORDER_LINES.PRODUCT_ID as fct_order_lines.PRODUCT_ID comment='Product sold on the line.',
		FCT_ORDER_LINES.ORDER_DATE as fct_order_lines.ORDER_DATE comment='Date the parent order was placed.',
		DIM_CUSTOMER.CUSTOMER_SK as dim_customer.CUSTOMER_SK comment='Customer surrogate key.',
		DIM_CUSTOMER.CUSTOMER_SEGMENT as dim_customer.CUSTOMER_SEGMENT comment='Marketing segment. Values: new = First-ever order not yet placed or just placed; returning = Has ordered before; vip = High-value repeat customer; prospect = Signed up, never ordered.',
		DIM_CUSTOMER.ACCEPTS_MARKETING as dim_customer.ACCEPTS_MARKETING comment='Whether the customer opted in to marketing.',
		DIM_PRODUCT.PRODUCT_ID as dim_product.PRODUCT_ID comment='Product surrogate key.',
		DIM_PRODUCT.CATEGORY as dim_product.CATEGORY comment='Product category.',
		DIM_PRODUCT.TITLE as dim_product.TITLE comment='Product title.',
		DIM_CHANNEL.CHANNEL_ID as dim_channel.CHANNEL_ID comment='Channel surrogate key.',
		DIM_CHANNEL.CHANNEL_NAME as dim_channel.CHANNEL_NAME comment='Channel display name.'
	)
	metrics (
		FCT_ORDERS.GROSS_REVENUE as SUM(FCT_ORDERS.ORDER_GROSS) comment='Gross order revenue.',
		FCT_ORDERS.NET_REVENUE as SUM(FCT_ORDERS.ORDER_NET_BOOKED) comment='Net booked revenue.',
		FCT_ORDERS.ORDERS as COUNT(DISTINCT FCT_ORDERS.ORDER_ID),
		FCT_ORDERS.REFUNDED_ORDERS as SUM(CASE WHEN FCT_ORDERS.IS_REFUNDED THEN 1 ELSE 0 END) comment='Count of refunded orders.',
		FCT_ORDERS.AOV as SUM(FCT_ORDERS.ORDER_NET_BOOKED) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID) comment='Average order value (net revenue / orders).',
		FCT_ORDERS.REFUND_RATE as SUM(CASE WHEN FCT_ORDERS.IS_REFUNDED THEN 1 ELSE 0 END) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID) comment='Refunded orders / all orders.',
		FCT_ORDER_LINES.UNITS_SOLD as SUM(FCT_ORDER_LINES.QUANTITY) comment='Units sold.',
		FCT_ORDER_LINES.UNITS_PER_ORDER as SUM(FCT_ORDER_LINES.QUANTITY) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID) comment='Units per order (cross-table).'
	)
;
```
