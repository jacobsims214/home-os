import EntityList from "@/components/entity/EntityList";
export default function BillsPage() {
  return <EntityList entityType="bill" title="Bills" description="Track recurring expenses" columns={[{name:"name",label:"Name"},{name:"amount",label:"Amount",format:"currency"},{name:"due_day",label:"Due"},{name:"category",label:"Category"}]} showMonthlyTotal propertyFilter />;
}
