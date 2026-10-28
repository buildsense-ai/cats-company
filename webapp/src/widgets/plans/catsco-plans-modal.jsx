import React from 'react';
import RelayAccessModal from '../relay-access-modal';
import EnterpriseTeam from './enterprise-team';
import EnterpriseServices from './enterprise-services';
import { personalPlanOffers, planPresentationOverrides, teamPlanOffer, tokenEstimateNote, resolvePlanOffers } from './plan-offers';
import './plan-chooser.css';

export default function CatsCoPlansModal(props) {
  return <RelayAccessModal
    {...props}
    showFreePlan={false}
    planOffers={personalPlanOffers}
    planPresentationOverrides={planPresentationOverrides}
    planChooserNote={tokenEstimateNote}
    enterprisePlanContent={({ onSelectPlan, catalogPlans, catalogEnabled }) => <>
      <EnterpriseTeam onSelectPlan={onSelectPlan} plan={resolvePlanOffers([teamPlanOffer], catalogPlans, catalogEnabled)[0]} />
      <EnterpriseServices />
    </>}
  />;
}
