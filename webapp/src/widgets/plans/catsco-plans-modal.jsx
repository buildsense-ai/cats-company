import React from 'react';
import RelayAccessModal from '../relay-access-modal';
import EnterpriseTeam from './enterprise-team';
import EnterpriseServices from './enterprise-services';
import { personalPlanOffers, planPresentationOverrides, teamPlanOffer, resolvePlanOffers } from './plan-offers';
import { creditPlanOffers, creditPlanPresentation, creditPlanNote } from './credit-plan-offers';
import './plan-chooser.css';

export default function CatsCoPlansModal(props) {
  return <RelayAccessModal
    {...props}
    showFreePlan={false}
    planOffers={personalPlanOffers}
    planChooserOffers={creditPlanOffers}
    personalPlanExcludedSlugs={[teamPlanOffer.slug]}
    planPresentationOverrides={{ ...planPresentationOverrides, ...creditPlanPresentation }}
    planChooserNote={creditPlanNote}
    teamPlanContent={({ onSelectPlan, catalogPlans, catalogEnabled }) =>
      <EnterpriseTeam onSelectPlan={onSelectPlan} plan={resolvePlanOffers([teamPlanOffer], catalogPlans, catalogEnabled)[0]} />}
    enterprisePlanContent={<EnterpriseServices />}
  />;
}
